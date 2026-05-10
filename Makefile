dev:
	pnpm --dir ui/web build && scripts/sync_embedded_web.sh \
	&& go build -ldflags "-s -w -X github.com/tim5wang/godex/internal/version.Version=v0.1.0" -o godex ./cmd/godex  \
	&& ./godex service uninstall && ./godex service install && ./godex service start
	 

build-linux:
	pnpm --dir ui/web build && scripts/sync_embedded_web.sh \
	&& GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X github.com/tim5wang/godex/internal/version.Version=v0.1.0" -o godex-linux-amd64 ./cmd/godex

deploy-linux:
	scp godex-linux-amd64 mycloud:/opt/godex/godex \
	&& ssh mycloud "cd /opt/godex && ./godex service uninstall && ./godex service install --addr 127.0.0.1:3800 && ./godex service start" \
	&& rm godex-linux-amd64