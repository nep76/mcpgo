.PHONY: build clean

NAME=mcpgo
OS=linux
ARCH=amd64

build:
	GOOS=$(OS) GOARCH=$(ARCH) CGO_ENABLED=0 go build -v -o $(NAME) .

clean:
	rm -f $(NAME)