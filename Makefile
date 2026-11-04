# Delegate targets to Taskfile.yml
.PHONY: all build test test-e2e lint ci serve tui clean

all: build

build test test-e2e lint ci serve tui clean:
	task $@
