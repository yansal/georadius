package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/mozillazg/go-unidecode"
	"github.com/redis/go-redis/v9"
)

var _ = index

func index() error {
	ctx := context.Background()

	r, err := newredis(ctx)
	if err != nil {
		return err
	}

	if err := addcoords(ctx, r); err != nil {
		return err
	}

	if err := addpops(ctx, r); err != nil {
		return err
	}

	if err := addcities(ctx, r); err != nil {
		return err
	}
	return nil
}

func addcoords(ctx context.Context, r *redis.Client) error {
	// TODO: embed data?
	b, err := os.ReadFile("data/cities.json")
	if err != nil {
		return err
	}
	var dest struct {
		Cities []struct {
			CityCode          string `json:"city_code"`           //: "ville du pont",
			DepartmentName    string `json:"department_name"`     //: "doubs",
			DepartmentNumber  string `json:"department_number"`   //: "25",
			InseeCode         string `json:"insee_code"`          //: "25620",
			Label             string `json:"label"`               //: "ville du pont",
			Latitude          string `json:"latitude"`            //: "46.999873398",
			Longitude         string `json:"longitude"`           //: "6.498147193",
			RegionGeojsonName string `json:"region_geojson_name"` //: "Bourgogne-Franche-Comt\u00e9"
			RegionName        string `json:"region_name"`         //: "bourgogne-franche-comt\u00e9",
			ZipCode           string `json:"zip_code"`            //: "25650",

		} `json:"cities"`
	}
	if err := json.Unmarshal(b, &dest); err != nil {
		return err
	}
	var (
		members      []any
		geolocations []*redis.GeoLocation
	)
	for i := range dest.Cities {
		lat, err := strconv.ParseFloat(dest.Cities[i].Latitude, 64)
		if err != nil {
			continue
		}
		lon, err := strconv.ParseFloat(dest.Cities[i].Longitude, 64)
		if err != nil {
			continue
		}
		key := normcitycode(dest.Cities[i].CityCode) + "-" + dest.Cities[i].DepartmentNumber

		n, err := r.HSet(ctx, key, "latitude", lat, "longitude", lon).Result()
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}

		members = append(members, key)
		geolocations = append(geolocations, &redis.GeoLocation{
			Name:      key,
			Latitude:  lat,
			Longitude: lon,
		})
	}
	if err := r.SAdd(ctx, "tmp:coords-set", members...).Err(); err != nil {
		return err
	}
	if err := r.GeoAdd(ctx, "coords", geolocations...).Err(); err != nil {
		return err
	}

	return nil
}

var (
	stprefixregexp  = regexp.MustCompile(`^st `)
	steprefixregexp = regexp.MustCompile(`^ste `)
	stregexp        = regexp.MustCompile(` st `)
	steregexp       = regexp.MustCompile(` ste `)
)

func normcitycode(s string) string {
	s = stprefixregexp.ReplaceAllString(s, "saint ")
	s = steprefixregexp.ReplaceAllString(s, "sainte ")
	s = stregexp.ReplaceAllString(s, " saint ")
	s = steregexp.ReplaceAllString(s, " sainte ")
	return strings.Join(strings.Fields(s), "-")
}

func addpops(ctx context.Context, r *redis.Client) error {
	b, err := os.ReadFile("data/donnees_communes.csv")
	if err != nil {
		return err
	}
	csvr := csv.NewReader(bytes.NewReader(b))
	csvr.Comma = ';'
	records, err := csvr.ReadAll()
	if err != nil {
		return err
	}
	var (
		members []any
		zs      []redis.Z
	)
	for i := range records {
		if i == 0 {
			continue
		}
		// TODO: handle " Arrondissement"
		pop, err := strconv.ParseFloat(records[i][7], 64)
		if err != nil {
			continue
		}
		key := normcom(records[i][6]) + "-" + records[i][2]

		n, err := r.HSet(ctx, key, "population", pop).Result()
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}

		members = append(members, key)
		zs = append(zs, redis.Z{
			Score:  pop,
			Member: key,
		})
	}
	if err := r.SAdd(ctx, "tmp:pops-set", members...).Err(); err != nil {
		return err
	}

	if err = r.ZAdd(ctx, "pops", zs...).Err(); err != nil {
		return err
	}

	return nil
}

func normcom(s string) string {
	s = strings.ReplaceAll(s, "'", " ")
	s = strings.ToLower(s)
	s = unidecode.Unidecode(s)
	return strings.Join(strings.Fields(s), "-")
}

func addcities(ctx context.Context, r *redis.Client) error {
	if err := r.SInterStore(ctx, "tmp:cities",
		"tmp:coords-set",
		"tmp:pops-set",
	).Err(); err != nil {
		return err
	}

	var cursor uint64
	for {
		var (
			keys []string
			err  error
		)
		keys, cursor, err = r.SScan(ctx, "tmp:cities", cursor, "", 0).Result()
		if err != nil {
			return err
		}
		var zs []redis.Z
		for i := range keys {
			for j := range keys[i] {
				zs = append(zs, redis.Z{Member: keys[i][:j+1]})
			}
			zs = append(zs, redis.Z{Member: keys[i] + "*"})
		}
		if err := r.ZAdd(ctx, "cities", zs...).Err(); err != nil {
			return err
		}
		if cursor == 0 {
			break
		}
	}
	return nil
}
