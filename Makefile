VERSION = 0.1
CI_PIPELINE_IID ?= 0
BUILD_VERSION = $(VERSION).$(CI_PIPELINE_IID)

build:
	GOOS=linux GOARCH=amd64 go build -o oracle-migrate .

build-linux:
	GOOS=linux GOARCH=amd64 go build -o oracle-migrate .

version:
	@echo $(BUILD_VERSION)

vendor:
	GOPATH=`pwd` go mod vend

copy:
	cp oracle-migrate ~/go/bin/oracle-migrate

.PHONY: build build-linux version vendor copy
