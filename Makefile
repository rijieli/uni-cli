BIN     := ../uni-cli
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
TARGETS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: build release clean

build:
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) .

# make release  -> dist/uni-cli_<version>_<os>_<arch>.{tar.gz,zip} + checksums.txt + notes.md
# publish:      gh release create <tag> dist/*.tar.gz dist/*.zip dist/checksums.txt --notes-file dist/notes.md
release:
	rm -rf dist && mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		name=uni-cli_$(VERSION)_$${os}_$${arch}; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$$name/uni-cli$$ext . || exit 1; \
		cp README.md dist/$$name/; \
		if [ $$os = windows ]; then (cd dist && zip -qr $$name.zip $$name); \
		else tar -C dist -czf dist/$$name.tar.gz $$name; fi; \
		rm -rf dist/$$name; \
	done
	cd dist && shasum -a 256 *.tar.gz *.zip > checksums.txt
	sed 's/{{VERSION}}/$(VERSION)/g' release-notes.md > dist/notes.md

clean:
	rm -rf $(BIN) dist
