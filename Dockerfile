FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY app ./app
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/cloudops ./app
# distroless には mkdir がないため、データベース用のフォルダはここで作り、nonroot の所有にしてコピーする
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/cloudops /app/cloudops
COPY config.example.json /app/config.example.json
COPY queries /app/queries
COPY --from=build --chown=65532:65532 /out/data /data
EXPOSE 8080
ENV CLOUDOPS_ADDRESS=0.0.0.0:8080
ENV CLOUDOPS_DB=/data/cloudops.db
USER nonroot:nonroot
ENTRYPOINT ["/app/cloudops"]
