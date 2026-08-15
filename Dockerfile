FROM node:24-alpine AS web
WORKDIR /src
COPY package.json package-lock.json* ./
RUN npm ci || npm install
COPY svelte.config.js tsconfig.json vite.config.ts ./
COPY web ./web
RUN npm run build

FROM golang:1.26-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/dist/app ./dist/app
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/issuetap ./cmd/issuetap

FROM alpine:3.22
RUN adduser -D -H issuetap && mkdir -p /data && chown issuetap:issuetap /data
USER issuetap
COPY --from=backend /out/issuetap /usr/local/bin/issuetap
EXPOSE 8080
ENV ISSUETAP_ADDR=0.0.0.0:8080
ENTRYPOINT ["issuetap"]
CMD ["serve"]
