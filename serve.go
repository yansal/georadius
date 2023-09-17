package main

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

var _ = serve

func serve() error {
	ctx := context.Background()
	r, err := newredis(ctx)
	if err != nil {
		return err
	}
	h := &handler{redis: r}
	http.Handle("/favicon.ico", http.NotFoundHandler())
	http.Handle("/", h)
	return http.ListenAndServe(":8080", nil)
}

type handler struct {
	redis *redis.Client
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("action") {
	case "query":
		h.query(w, r)
	case "search":
		h.search(w, r)
	default:
		if err := parseexecandwrite(w, "index.html", nil); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

const maxitems = 200

func (h *handler) query(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	radius, err := strconv.ParseFloat(query.Get("radius"), 64)
	if err != nil {
		radius = 50
	}
	input := query.Get("input")
	cities, err := h.queryredis(ctx, input, radius, query.Get("population"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	inputcity, err := h.getcity(ctx, input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var (
		querydata = &QueryData{
			Cities: cities,
			Input:  inputcity,
			Radius: radius * 1000,
		}
		data    any = querydata
		pattern     = "index.html"
	)
	if r.Header.Get("HX-Request") == "true" {
		pattern = "partials/query.html"
	} else {
		data = IndexData{Query: querydata}
	}
	if err := parseexecandwrite(w, pattern, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *handler) queryredis(ctx context.Context, input string, radius float64, population string) ([]City, error) {
	var zrange *redis.StringSliceCmd
	if _, err := h.redis.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.GeoSearchStore(ctx, "coords", "tmp:coords", &redis.GeoSearchStoreQuery{
			GeoSearchQuery: redis.GeoSearchQuery{
				Member:     input,
				Radius:     radius,
				RadiusUnit: "km",
			},
		})

		p.ZRangeStore(ctx, "tmp:pops", redis.ZRangeArgs{
			Key:     "pops",
			Start:   population,
			Stop:    "+inf",
			ByScore: true,
		})

		p.ZInterStore(ctx, "tmp:zinter", &redis.ZStore{
			Keys:    []string{"tmp:coords", "tmp:pops"},
			Weights: []float64{0, 1},
		})

		zrange = p.ZRevRange(ctx, "tmp:zinter", 0, maxitems)
		return nil
	}); err != nil {
		return nil, err
	}
	res, err := zrange.Result()
	if err != nil {
		return nil, err
	}
	var cities []City
	for i := range res {
		if res[i] == input {
			continue
		}
		cities = append(cities, City{ID: res[i]})
	}

	if err := h.loadcities(ctx, cities); err != nil {
		return nil, err
	}
	return cities, nil
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	input := r.FormValue("input")
	cities, err := h.searchredis(ctx, input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var (
		searchdata     = &SearchData{Cities: cities}
		data       any = searchdata
		pattern        = "index.html"
	)
	if r.Header.Get("HX-Request") == "true" {
		pattern = "partials/search.html"
	} else {
		data = IndexData{Search: searchdata}
	}
	if err := parseexecandwrite(w, pattern, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type IndexData struct {
	Query  *QueryData
	Search *SearchData
}

type QueryData struct {
	Cities []City
	Input  *City
	Radius float64
}

type SearchData struct {
	Cities []City
}

func (h *handler) searchredis(ctx context.Context, input string) ([]City, error) {
	if len(input) <= 2 {
		return nil, nil
	}
	rank, err := h.redis.ZRank(ctx, "cities", input).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cities []City
	for {
		start := rank
		stop := rank + 50 - 1
		zrange, err := h.redis.ZRange(ctx, "cities", start, stop).Result()
		if err != nil {
			return nil, err
		}
		var done bool
		for i := range zrange {
			if !strings.HasPrefix(zrange[i], input) {
				done = true
				break
			}
			if strings.HasSuffix(zrange[i], "*") {
				cities = append(cities, City{ID: strings.TrimSuffix(zrange[i], "*")})
			}
			if len(cities) >= maxitems {
				done = true
				break
			}
		}
		if len(zrange) < 50 || done {
			break
		}
		rank += 50
	}
	if err := h.loadcities(ctx, cities); err != nil {
		return nil, err
	}
	return cities, nil
}

func (h *handler) loadcities(ctx context.Context, cities []City) error {
	var cmds []*redis.MapStringStringCmd
	if _, err := h.redis.TxPipelined(ctx, func(p redis.Pipeliner) error {
		for i := range cities {
			cmds = append(cmds, p.HGetAll(ctx, cities[i].ID))
		}
		return nil
	}); err != nil {
		return err
	}
	for i := range cmds {
		res, err := cmds[i].Result()
		if err != nil {
			return err
		}
		loadcity(&cities[i], res)
	}
	return nil
}

func (h *handler) getcity(ctx context.Context, input string) (*City, error) {
	city := City{
		ID: input,
	}
	res, err := h.redis.HGetAll(ctx, city.ID).Result()
	if err != nil {
		return nil, err
	}
	loadcity(&city, res)
	return &city, nil
}

func loadcity(city *City, res map[string]string) {
	city.Lat = res["latitude"]
	city.Lon = res["longitude"]
	city.Population = res["population"]
}

type City struct {
	ID         string
	Lat, Lon   string
	Population string
}

func parseexecandwrite(w http.ResponseWriter, pattern string, data any) error {
	fs := os.DirFS("templates")
	base, err := template.ParseFS(fs, "*.html", "partials/*.html")
	if err != nil {
		return err
	}

	b := new(bytes.Buffer)
	if strings.HasPrefix(pattern, "partials/") {
		if err := base.ExecuteTemplate(b, pattern, data); err != nil {
			return err
		}
	} else {
		if err := base.Execute(b, data); err != nil {
			return err
		}

	}
	w.Write(b.Bytes())
	return nil
}
