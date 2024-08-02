#!/bin/bash

# Define the path to the proto files and the destination directory
PROTO_DIR="./proto"
DEST_DIR="./pkgs"

# Create the destination directory if it does not exist
mkdir -p $DEST_DIR

# Iterate over each .proto file in the proto directory
for PROTO_FILE in $PROTO_DIR/*.proto; do
    # Run the protoc command for each file
    protoc --go_out=$DEST_DIR --go_opt=paths=source_relative --go-grpc_out=$DEST_DIR --go-grpc_opt=paths=source_relative $PROTO_FILE --grpc-gateway_out=paths=source_relative $PROTO_FILE --proto_path=$PROTO_DIR $PROTO_FILE
done

echo "Proto files have been generated in $DEST_DIR"
