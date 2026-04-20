# syntax=docker/dockerfile:1

# --- build stage -----------------------------------------------------------
FROM node:20 AS build
RUN apt-get update && apt-get install -y --no-install-recommends git \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
ARG PINGPUB_SHA=ee1ca5ee54d7bd2dc4c5b4df8f3b93595440e9c8
RUN git clone https://github.com/ping-pub/explorer.git . \
 && git checkout ${PINGPUB_SHA} \
 && rm -f chains/mainnet/*.json chains/testnet/*.json
# ping-pub defaults to "mainnet" network on localhost (only scans chains/mainnet/
# when hostname doesn't contain "testnet"), so our chain lives there too.
COPY svote.json chains/mainnet/svote.json
COPY zcash.svg public/logos/zcash.svg
RUN corepack enable \
 && yarn install \
 && yarn build

# --- runtime stage ---------------------------------------------------------
FROM nginx:alpine
COPY --from=build /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
