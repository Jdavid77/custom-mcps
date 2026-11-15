FROM golang:1.27.4-alpine AS build
ARG MCP
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY ${MCP}/ ${MCP}/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mcp ./${MCP}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /mcp /mcp
EXPOSE 8080
ENTRYPOINT ["/mcp"]
