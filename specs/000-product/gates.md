# Gates

| Gate | Command | Pass |
| --- | --- | --- |
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Test | `go test ./... -count=1` | exit 0 |
| Typecheck | `npm run typecheck` | exit 0 |
| Web build | `npm run build` | `dist/app/index.html` exists |
| Secretscan | `make secretscan` | `secretscan: clean` |
| Gadak | `make test-gadak` | mirror matches fixture; 401 stops; 429 retries |
| Sibling trees | `git -C ../gadak status --short` (and billtap, dogtap) | empty |
