package main

import (
	"context"
	"flag"
	"log"

	"github.com/redis/go-redis/v9"
)

func main() {
	doindex := flag.Bool("index", false, "")
	flag.Parse()
	if *doindex {
		if err := index(); err != nil {
			log.Fatal(err)
		}
	}
	if err := serve(); err != nil {
		log.Fatal(err)
	}
}

func newredis(ctx context.Context) (*redis.Client, error) {
	r := redis.NewClient(&redis.Options{})
	if err := r.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return r, nil
}
