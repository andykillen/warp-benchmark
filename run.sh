#!/bin/bash

go build -buildvcs=false -o warp-bench .;
export BENCH_COOKIE_LOGGED_IN='wordpress_logged_in_256cd14049b13d565fe9ae359b7e7b34=andrewkillen%7C1791746825%7CXUvouTSvFQmiuwCP1W481yCkrRyy48jlVaUSyl8anpH%7C3ca6b54971a3948336ae1ffd49c370164301983bfc285f4ca8a7cf96cec6a5a5'

./warp-bench -config config.json -plan
./warp-bench -config config.json
Workload plans: results/278f3d9d8f29b84de4a604ac