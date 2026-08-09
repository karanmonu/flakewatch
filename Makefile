.PHONY: build test vet

build:
	go build -o flakewatch .

test:
	go test -race -cover ./...

vet:
	go vet ./...

