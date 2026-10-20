FROM ghcr.io/foundry-rs/foundry:v1.0.0
USER root
WORKDIR /contracts
COPY foundry.toml ./
COPY src ./src
COPY test ./test
COPY deploy.sh ./
RUN forge install foundry-rs/forge-std@v1.9.4 --no-git && forge build
ENTRYPOINT ["/bin/sh"]
CMD ["deploy.sh"]
