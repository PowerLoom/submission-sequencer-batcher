#!/bin/bash

env_vars=""

env_file=".env"
if [ -f "$env_file" ]; then
  echo "Reading $env_file"
  while IFS= read -r line || [ -n "$line" ]; do
    if [[ ! "$line" =~ ^# && -n "$line" ]]; then
      env_vars+=" $line"
    fi
  done < "$env_file"
else
  echo "File $env_file does not exist"
fi

env $env_vars go run cmd/main.go
