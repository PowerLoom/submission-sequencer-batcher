# Sequencer Deployment
Scripts to deploy PowerLoom Sequencer

## Requirements

1. Latest version of `docker` (`>= 20.10.21`) and `docker-compose` (`>= v2.13.0`)
2. At least 4 core CPU, 8GB RAM and 50GB SSD - make sure to choose the correct spec when deploying to Github Codespaces.
3. IPFS node
    - While we have __included__ a node in our autobuild docker setup, IPFS daemon can hog __*a lot*__ of resources - it is not recommended to run this on a personal computer unless you have a strong internet connection and dedicated CPU+RAM.
    - 3rd party IPFS services that provide default IFPS interface like Infura are now supported.



## Running the Sequencer Node

Clone the repository against the testnet branch.

`git clone https://github.com/PowerLoom/proto-snapshot-collector.git --single-branch powerloom_sequencer --branch lite_node_test && cd powerloom_sequencer`


### Deployment steps

1. Copy `env.example` to `.env`.
    - Ensure the following required variables are filled:
        - `SIGNER_ACCOUNT_ADDRESS`: The address of the signer account. This is your whitelisted address on the protocol. **Using a burner account is highly recommended**
        - `SIGNER_ACCOUNT_PRIVATE_KEY`: The private key corresponding to the signer account address.
        - `PROST_CHAIN_ID`: The chain ID for the PROST RPC service.
        - `RENDEZVOUS_POINT`: The identifier for locating all relayer peers which are the only way to access the sequencer and submit snapshots.
        - `PROTOCOL_STATE_CONTRACT`: The contract address for the protocol state.
        - `PROST_RPC_URL`: The URL for the PROST RPC service.
        - `IPFS_URL`: The URL for the IPFS (InterPlanetary File System) service in HTTP(s) (e.g. `https://ipfs.infura.io:5001`) multiaddr format (e.g. `/dns4/ipfs.infura.io/tcp/5001/https`)

    - Optionally, you may also set the following variables:
        - `BATCH_SIZE`: The number of submissions to be included per batch.
        - `BLOCK_TIME`: The block time of the chain.
        - `REDIS_HOST` & `REDIS_PORT` & `REDIS_DB`: The redis server connection url (if you wish to use a separate one).
        - `FULL_NODES`: A list of full nodes in case of fallback mode

2. Build the image

   `./build-docker.sh`

3. Run the following command (ideally in a `screen`) and follow instructions

   `./run.sh`

## Troubleshooting
### To be added
### Stopping and Resetting
1. To shutdown services, just press `Ctrl+C` (and again to force).