#!/usr/bin/env bash

VERSION="$1"

# Check if the version is a valid semver
if echo "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  # Add the "v" prefix if it doesn't already exist
  VERSION="v$VERSION"
elif ! echo "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "Error: '$VERSION' is not a valid semver string." >&2
  exit 1
fi

echo "$VERSION"
