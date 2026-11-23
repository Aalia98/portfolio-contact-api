FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /out/api .

FROM alpine:3.20
RUN adduser -D app
USER app
COPY --from=build /out/api /api
ENV PORT=8080
EXPOSE 8080
CMD ["/api"]
