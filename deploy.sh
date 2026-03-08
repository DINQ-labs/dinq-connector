#!/bin/bash
set -e

IMAGE="ghcr.io/dinq-labs/dinq-connector:latest"
PLATFORM="linux/amd64"

case "${1:-deploy}" in
  build)
    echo "Building $IMAGE..."
    docker build --platform $PLATFORM -t $IMAGE .
    ;;
  push)
    echo "Pushing $IMAGE..."
    docker push $IMAGE
    ;;
  deploy)
    echo "Building and pushing $IMAGE..."
    docker build --platform $PLATFORM -t $IMAGE .
    docker push $IMAGE
    echo "Done. Run on server:"
    echo "  docker pull $IMAGE && docker compose up -d"
    ;;
  up)
    git pull
    docker compose up -d --build
    ;;
  down)
    docker compose down
    ;;
  logs)
    docker compose logs -f dinq-connector
    ;;
  *)
    echo "Usage: $0 {build|push|deploy|up|down|logs}"
    exit 1
    ;;
esac
