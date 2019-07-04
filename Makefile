GOPATH := $(shell go env GOPATH)
GODEP  := $(GOPATH)/bin/dep
GOLINT := $(GOPATH)/bin/golint
BINARY_NAME := rtsp-proxy
PORT   := 8554
packages = $$(go list ./... | egrep -v '/vendor/' | egrep -v '/cmd/')
files = $$(find . -name '*.go' | egrep -v '/vendor/' | egrep -v '/cmd/')

.PHONY: all help run vet lint build install

all: build

help:           ## Show this help
	@echo "Usage:"
	@fgrep -h "##" $(MAKEFILE_LIST) | fgrep -v fgrep | sed -e 's/\\$$//' | sed -e 's/##//'

install:        ## Install binary
	cp $(BINARY_NAME) /usr/bin/

build:          ## Build the binary
build: vendor lint vet
	go build -o $(BINARY_NAME) cmd/main.go

run:            ## run script with arguments. example: `make run -- arg1 arg2`
run:
	go run cmd/main.go $(filter-out $@, $(MAKECMDGOALS)) -port $(PORT)

vet:            ## Run go vet
vet:
	go vet -printfuncs=Debug,Debugf,Debugln,Info,Infof,Infoln,Error,Errorf,Errorln $(files)

lint:           ## Run go lint
lint: $(GOLINT)
	$(GOLINT) -set_exit_status $(packages)

%:
	@true

$(GODEP):
	cd $(GOPATH) && go get -u github.com/golang/dep/cmd/dep

$(GOLINT):
	cd $(GOPATH) && go get -u golang.org/x/lint/golint
	cd $(GOPATH) && go get -u github.com/golang/lint/golint
