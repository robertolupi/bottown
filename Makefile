.PHONY: build test e2e vet

build:
	go build -o bin/bottown ./cmd/bottown

vet:
	go vet ./... && go vet -tags e2e ./e2e/

test:
	go test ./...

# Needs Docker. Uses throwaway e2e-* instances; never touches prod or dev.
e2e:
	go test -tags e2e -count=1 -timeout 30m -v ./e2e/
