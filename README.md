❗❗❗Updates: This repository is no longer maintained.
If you wish to use it, please merge BSC releases after v1.5.11 and update the code as needed.

## Overview

The BSC network has introduced the [Builder API Specification](https://github.com/bnb-chain/BEPs/blob/master/BEPs/BEP322.md) to establish a fair and unified MEV market. Previously, BSC clients lacked native support for validators to integrate with multiple MEV providers at once. The network became unstable because of the many different versions of the client software being used. The latest BSC client adopts the [Proposer-Builder Separation](https://ethereum.org/en/roadmap/pbs/) model. Within this unified framework, several aspects of the BSC network have been improved:

- Stability: Validators only need to use the official client to seamlessly integrate with various Builders.
- Economy: Builders that can enter without permission promote healthy market competition. Validators can extract more value by integrating with more builders, which benefits delegators as well.
- Transparency: This specification aims to bring transparency to the BSC MEV market, exposing profit distribution among stakeholders to the public.

This project represents a minimal implementation of the protocol and is provided as is. We make no guarantees regarding its functionality or security.

## What is MEV and PBS

MEV, also known as Maximum (or Miner) Extractable Value, can be described as the measure of total value that may be extracted from transaction ordering. Common examples include arbitraging swaps on decentralized exchanges or identifying opportunities to liquidate DeFi positions. Maximizing MEV requires advanced technical expertise and custom software integrated into regular validators. The returns are likely higher with centralized operators.

Proposer-builder separation(PBS) solves this problem by reconfiguring the economics of MEV. Block builders create blocks and submit them to the block proposer, and the block proposer simply chooses the most profitable one, paying a fee to the block builder. This means even if a small group of specialized block builders dominate MEV extraction, the reward still goes to any validator on the network.

## Release Types
There are three types of release, each with a clear purpose and version scheme:

- **1.Stable Release**: production-ready builds for the vast majority of users.  Format: `v<Major>.<Minor>.<Patch>`, example: [v1.5.19](https://github.com/bnb-chain/bsc/releases/tag/v1.5.19).
- **2.Feature Release**: early access to a single feature without affecting the core product. Format: `v<Major>.<Minor>.<Patch>-feature-<FeatureName>`, example: [v1.5.19-feature-SI](https://github.com/bnb-chain/bsc/releases/tag/v1.5.19-feature-SI).
- **3.Preview Release**: bleeding-edge builds for users who want the latest code. Format: `v<Major>.<Minor>.<Patch>-<Meta>`, Meta values indicate maturity: alpha (experimental), beta (largely complete), rc (release candidate), example: [v1.5.0-alpha](https://github.com/bnb-chain/bsc/releases/tag/v1.5.0-alpha).

## Key features

![PBS Workflow](./docs/assets/pbs_workflow.png)

The figure above illustrates the basic workflow of PBS operating on the BSC network.

- MEV Searchers are independent network participants who detect profitable MEV opportunities and submit their transactions to builders. Transactions from searchers are usually bundled together and included in a block, or none of them will be included.
- The builder collects transactions from various sources to create an unsealed block and offer it to the block proposer. The builder will specify in the request the amount of fees the proposer needs to pay to the builder if this block is adopted. The unsealed block from the builder is also called a block bid as it may request tips.
- The proposer chooses the most profitable block from multiple builders, and pays the fee to the builder by appending a payment transaction at the end of the block.

A new component called "Sentry" has been introduced to enhance network security and account isolation. It assists proposers in communicating with builders and enables payment processing.

## What is More

The PBS model on BSC differs in several aspects from its implementation on Ethereum. This is primarily due to：

1. Different Trust Model. Validators in the BNB Smart Chain are considered more trustworthy, as it requires substantial BNB delegation and must maintain a high reputation. This stands in contrast to Ethereum, where becoming an Ethereum validator is much easier, the barrier to becoming a validator is very low (i.e., 32 ETH).
2. Different Consensus Algorithms. In Ethereum, a block header is transferred from a builder to a validator for signing, allowing the block to be broadcasted to the network without disclosing the transactions to the validator. In contrast, in BSC, creating a valid block header requires executing transactions and system contract calls (such as transferring reward and depositing to the validator set contract), making it impossible for builders to propose the whole block.
3. Different Blocking Time. With a shorter block time of 3 seconds in BSC compared to Ethereum's 12 seconds, designing for time efficiency becomes crucial.

These differences have led to different designs on BSC's PBS regarding payment, interaction, and APIs. For more design philosophy, please refer to [BEP322:Builder API Specification for BNB Smart Chain](https://github.com/bnb-chain/BEPs/blob/master/BEPs/BEP322.md).

## Integration Guide for Builder

The [Builder API Specification](https://github.com/bnb-chain/BEPs/blob/master/BEPs/BEP322.md) defines the standard interface that builders should implement, while the specific implementation is left open to MEV API providers. The BNB Chain community offers a [simple implementation](https://github.com/bnb-chain/bsc-builder) example for reference.

### Customize Builder

Building `geth` requires both a Go (version 1.24 or later) and a C compiler (GCC 5 or higher). You can install
them using your favourite package manager. Once the dependencies are installed, run

1. The builder needs to set up a builder account, which is used to sign the block bid and receive fees. The builder can ask for a tip (builder fee) on the block that it sends to the sentry. If the block is finally selected, the builder account will receive the tip.
2. The builder needs to implement the mev_reportIssue API to receive the errors report from validators.
3. In order to prevent transaction leakage, the builder can only send block bids to the in-turn validator.
4. At most 3 block bids are allowed to be sent at the same height from the same builder.

Here are some sentry APIs that may interest a builder:

1. `mev_bestBidGasFee`. It will return the current most profitable reward that the validator received among all the blocks received from all builders. The reward is calculated as: `gasFee*(1 - commissionRate) - tipToBuilder`. A builder may compare the `bestBidGasFee` with a local one and then decide to send the block bid or not.
2. `mev_params`. It will return the `BidSimulationLeftOver`,`ValidatorCommission`, `GasCeil` and `BidFeeCeil` settings on the validator. If the current time is after `(except block time - BidSimulationLeftOver)`, then there is no need to send block bids anymore; `ValidatorCommission` and `BidFeeCeil` helps the builder to build its fee charge strategy. The `GasCeil` helps a builder know when to stop adding more transactions.

Builders have the freedom to define various aspects like pricing models for users, creating intuitive APIs, and define the bundle verification rules.

### Setup with Example Builder

Step 1: Find Validator Information
For validators that open MEV integration, the public information is shown at [bsc-mev-info](https://github.com/bnb-chain/bsc-mev-info). Builders can also provide information here to the validator.

Step 2: Set up Builder.
The builder must sign the bid using an account, such as the etherbase account specified in the config.toml file.

```toml
[Eth.Miner.Mev]
BuilderEnabled = true # open bid sending
BuilderAccount = "0x..." # builder address which signs bid, usually it is the same as etherbase address

# Configure the validator node list, including the address of the validator and the public URL. The public URL refers to the sentry service.
[[Eth.Miner.Mev.Validators]]
Address = "0x23707D3D71E31e4Cb5B4A9816DfBDCA6455B52B3"
URL = "https://bsc-fuji.io"

[[Eth.Miner.Mev.Validators]]
Address = "0x..."
URL = "https://bsc-mathwallet.io"
```

Step 3: Startup

command example:
```
nohup /server/bsc/builder/geth_builder --config /server/bsc/builder/config.toml --datadir /server/bsc/builder/ --password /server/bsc/builder/password.txt --nodekey /server/bsc/builder/geth/nodekey -unlock 0x24e1652D280dAC12e402BEa00bECCab1D11A25B2 --miner.etherbase 0x24e1652D280dAC12e402BEa00bECCab1D11A25B2 --rpc.allow-unprotected-txs --allow-insecure-unlock --ws.addr 0.0.0.0 --ws.port 8546 --http.addr 0.0.0.0 --http.port 8546 --metrics.addr 0.0.0.0 --metrics.port 6061 --metrics.expensive --gcmode archive --state.scheme hash --syncmode full --mine  --monitor.maliciousvote --port 35555 --tries-verify-mode none > bsc-node.log &
```
notice: builder is a fullnode, and should using `--mine` to open block building.

please try to add the following environment variables and build again:
```shell
export CGO_CFLAGS="-O -D__BLST_PORTABLE__" 
export CGO_CFLAGS_ALLOW="-O -D__BLST_PORTABLE__"
```

## Executables

The bsc project comes with several wrappers/executables found in the `cmd`
directory.

|  Command   | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| :--------: | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **`geth`** | Main BNB Smart Chain client binary. It is the entry point into the BSC network (main-, test- or private net), capable of running as a full node (default), archive node (retaining all historical state) or a light node (retrieving data live). It has the same and more RPC and other interface as go-ethereum and can be used by other processes as a gateway into the BSC network via JSON RPC endpoints exposed on top of HTTP, WebSocket and/or IPC transports. `geth --help` and the [CLI page](https://geth.ethereum.org/docs/interface/command-line-options) for command line options. |
|   `clef`   | Stand-alone signing tool, which can be used as a backend signer for `geth`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
|  `devp2p`  | Utilities to interact with nodes on the networking layer, without running a full blockchain.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
|  `abigen`  | Source code generator to convert Ethereum contract definitions into easy to use, compile-time type-safe Go packages. It operates on plain [Ethereum contract ABIs](https://docs.soliditylang.org/en/develop/abi-spec.html) with expanded functionality if the contract bytecode is also available. However, it also accepts Solidity source files, making development much more streamlined. Please see our [Native DApps](https://geth.ethereum.org/docs/dapp/native-bindings) page for details.                                                                                               |
| `bootnode` | Stripped down version of our Ethereum client implementation that only takes part in the network node discovery protocol, but does not run any of the higher level application protocols. It can be used as a lightweight bootstrap node to aid in finding peers in private networks.                                                                                                                                                                                                                                                                                                            |
|   `evm`    | Developer utility version of the EVM (Ethereum Virtual Machine) that is capable of running bytecode snippets within a configurable environment and execution mode. Its purpose is to allow isolated, fine-grained debugging of EVM opcodes (e.g. `evm --code 60ff60ff --debug run`).                                                                                                                                                                                                                                                                                                            |
| `rlpdump`  | Developer utility tool to convert binary RLP ([Recursive Length Prefix](https://ethereum.org/en/developers/docs/data-structures-and-encoding/rlp)) dumps (data encoding used by the Ethereum protocol both network as well as consensus wise) to user-friendlier hierarchical representation (e.g. `rlpdump --hex CE0183FFFFFFC4C304050583616263`).                                                                                                                                                                                                                                                                                 |

## Running `geth`

Going through all the possible command line flags is out of scope here (please consult our
[CLI Wiki page](https://geth.ethereum.org/docs/fundamentals/command-line-options)),
but we've enumerated a few common parameter combos to get you up to speed quickly
on how you can run your own `geth` instance.

### Hardware Requirements

The hardware must meet certain requirements to run a full node on mainnet:
- VPS running recent versions of Mac OS X, Linux, or Windows.
- IMPORTANT 3 TB(Dec 2023) of free disk space, solid-state drive(SSD), gp3, 8k IOPS, 500 MB/S throughput, read latency <1ms. (if node is started with snap sync, it will need NVMe SSD)
- 16 cores of CPU and 64 GB of memory (RAM)
- Suggest m5zn.6xlarge or r7iz.4xlarge instance type on AWS, c2-standard-16 on Google cloud.
- A broadband Internet connection with upload/download speeds of 5 MB/S

The requirement for testnet:
- VPS running recent versions of Mac OS X, Linux, or Windows.
- 500G of storage for testnet.
- 4 cores of CPU and 16 gigabytes of memory (RAM).

### Steps to Run a Fullnode

#### 1. Download the pre-build binaries
```shell
# Linux
wget $(curl -s https://api.github.com/repos/bnb-chain/bsc/releases/latest |grep browser_ |grep geth_linux |cut -d\" -f4)
mv geth_linux geth
chmod -v u+x geth

# MacOS
wget $(curl -s https://api.github.com/repos/bnb-chain/bsc/releases/latest |grep browser_ |grep geth_mac |cut -d\" -f4)
mv geth_macos geth
chmod -v u+x geth
```

#### 2. Download the config files
```shell
//== mainnet
wget $(curl -s https://api.github.com/repos/bnb-chain/bsc/releases/latest |grep browser_ |grep mainnet |cut -d\" -f4)
unzip mainnet.zip

//== testnet
wget $(curl -s https://api.github.com/repos/bnb-chain/bsc/releases/latest |grep browser_ |grep testnet |cut -d\" -f4)
unzip testnet.zip
```

#### 3. Download snapshot
Download latest chaindata snapshot from [here](https://github.com/bnb-chain/bsc-snapshots). Follow the guide to structure your files.

#### 4. Start a full node
```shell
## It will run with Path-Base Storage Scheme by default and enable inline state prune, keeping the latest 90000 blocks' history state.
./geth --config ./config.toml --datadir ./node  --cache 8000 --rpc.allow-unprotected-txs --history.transactions 0

## It is recommend to run fullnode with `--tries-verify-mode none` if you want high performance and care little about state consistency.
./geth --config ./config.toml --datadir ./node  --cache 8000 --rpc.allow-unprotected-txs --history.transactions 0 --tries-verify-mode none
```

#### 5. Monitor node status

Monitor the log from **./node/bsc.log** by default. When the node has started syncing, should be able to see the following output:
```shell
t=2022-09-08T13:00:27+0000 lvl=info msg="Imported new chain segment"             blocks=1    txs=177   mgas=17.317   elapsed=31.131ms    mgasps=556.259  number=21,153,429 hash=0x42e6b54ba7106387f0650defc62c9ace3160b427702dab7bd1c5abb83a32d8db dirty="0.00 B"
t=2022-09-08T13:00:29+0000 lvl=info msg="Imported new chain segment"             blocks=1    txs=251   mgas=39.638   elapsed=68.827ms    mgasps=575.900  number=21,153,430 hash=0xa3397b273b31b013e43487689782f20c03f47525b4cd4107c1715af45a88796e dirty="0.00 B"
t=2022-09-08T13:00:33+0000 lvl=info msg="Imported new chain segment"             blocks=1    txs=197   mgas=19.364   elapsed=34.663ms    mgasps=558.632  number=21,153,431 hash=0x0c7872b698f28cb5c36a8a3e1e315b1d31bda6109b15467a9735a12380e2ad14 dirty="0.00 B"
```

#### 6. Interact with fullnode
Start up `geth`'s built-in interactive [JavaScript console](https://geth.ethereum.org/docs/interface/javascript-console),
(via the trailing `console` subcommand) through which you can interact using [`web3` methods](https://web3js.readthedocs.io/en/) 
(note: the `web3` version bundled within `geth` is very old, and not up to date with official docs),
as well as `geth`'s own [management APIs](https://geth.ethereum.org/docs/rpc/server).
This tool is optional and if you leave it out you can always attach to an already running
`geth` instance with `geth attach`.

#### 7. More

More details about [running a node](https://docs.bnbchain.org/bnb-smart-chain/developers/node_operators/full_node/) and [becoming a validator](https://docs.bnbchain.org/bnb-smart-chain/validator/create-val/)

*Note: Although some internal protective measures prevent transactions from
crossing over between the main network and test network, you should always
use separate accounts for play and real money. Unless you manually move
accounts, `geth` will by default correctly separate the two networks and will not make any
accounts available between them.*

### Configuration

As an alternative to passing the numerous flags to the `geth` binary, you can also pass a
configuration file via:

```shell
$ geth --config /path/to/your_config.toml
```

To get an idea of how the file should look like you can use the `dumpconfig` subcommand to
export your existing configuration:

```shell
$ geth --your-favourite-flags dumpconfig
```

### Programmatically interfacing `geth` nodes

As a developer, sooner rather than later you'll want to start interacting with `geth` and the
BSC network via your own programs and not manually through the console. To aid
this, `geth` has built-in support for a JSON-RPC based APIs ([standard APIs](https://ethereum.org/en/developers/docs/apis/json-rpc/),
[`geth` specific APIs](https://geth.ethereum.org/docs/interacting-with-geth/rpc), and [BSC's JSON-RPC API Reference](rpc/json-rpc-api.md)).
These can be exposed via HTTP, WebSockets and IPC (UNIX sockets on UNIX based
platforms, and named pipes on Windows).

The IPC interface is enabled by default and exposes all the APIs supported by `geth`,
whereas the HTTP and WS interfaces need to manually be enabled and only expose a
subset of APIs due to security reasons. These can be turned on/off and configured as
you'd expect.

HTTP based JSON-RPC API options:

  * `--http` Enable the HTTP-RPC server
  * `--http.addr` HTTP-RPC server listening interface (default: `localhost`)
  * `--http.port` HTTP-RPC server listening port (default: `8545`)
  * `--http.api` API's offered over the HTTP-RPC interface (default: `eth,net,web3`)
  * `--http.corsdomain` Comma separated list of domains from which to accept cross-origin requests (browser enforced)
  * `--ws` Enable the WS-RPC server
  * `--ws.addr` WS-RPC server listening interface (default: `localhost`)
  * `--ws.port` WS-RPC server listening port (default: `8546`)
  * `--ws.api` API's offered over the WS-RPC interface (default: `eth,net,web3`)
  * `--ws.origins` Origins from which to accept WebSocket requests
  * `--ipcdisable` Disable the IPC-RPC server
  * `--ipcpath` Filename for IPC socket/pipe within the datadir (explicit paths escape it)

You'll need to use your own programming environments' capabilities (libraries, tools, etc) to
connect via HTTP, WS or IPC to a `geth` node configured with the above flags and you'll
need to speak [JSON-RPC](https://www.jsonrpc.org/specification) on all transports. You
can reuse the same connection for multiple requests!

**Note: Please understand the security implications of opening up an HTTP/WS based
transport before doing so! Hackers on the internet are actively trying to subvert
BSC nodes with exposed APIs! Further, all browser tabs can access locally
running web servers, so malicious web pages could try to subvert locally available
APIs!**

### Operating a private network
- [BSC-Deploy](https://github.com/bnb-chain/node-deploy/): deploy tool for setting up BNB Smart Chain.

## Running a bootnode

Bootnodes are super-lightweight nodes that are not behind a NAT and are running just discovery protocol. When you start up a node it should log your enode, which is a public identifier that others can use to connect to your node. 

First the bootnode requires a key, which can be created with the following command, which will save a key to boot.key:

```
bootnode -genkey boot.key
```

This key can then be used to generate a bootnode as follows:

```
bootnode -nodekey boot.key -addr :30311 -network bsc
```

The choice of port passed to -addr is arbitrary. 
The bootnode command returns the following logs to the terminal, confirming that it is running:

```
enode://3063d1c9e1b824cfbb7c7b6abafa34faec6bb4e7e06941d218d760acdd7963b274278c5c3e63914bd6d1b58504c59ec5522c56f883baceb8538674b92da48a96@127.0.0.1:0?discport=30311
Note: you're using cmd/bootnode, a developer tool.
We recommend using a regular node as bootstrap node for production deployments.
INFO [08-21|11:11:30.687] New local node record                    seq=1,692,616,290,684 id=2c9af1742f8f85ce ip=<nil> udp=0 tcp=0
INFO [08-21|12:11:30.753] New local node record                    seq=1,692,616,290,685 id=2c9af1742f8f85ce ip=54.217.128.118 udp=30311 tcp=0
INFO [09-01|02:46:26.234] New local node record                    seq=1,692,616,290,686 id=2c9af1742f8f85ce ip=34.250.32.100  udp=30311 tcp=0
```

## Contribution

Thank you for considering helping out with the source code! We welcome contributions
from anyone on the internet, and are grateful for even the smallest of fixes!

If you'd like to contribute to bsc, please fork, fix, commit and send a pull request
for the maintainers to review and merge into the main code base. If you wish to submit
more complex changes though, please check up with the core devs first on [our discord channel](https://discord.gg/bnbchain)
to ensure those changes are in line with the general philosophy of the project and/or get
some early feedback which can make both your efforts much lighter as well as our review
and merge procedures quick and simple.

Please make sure your contributions adhere to our coding guidelines:

 * Code must adhere to the official Go [formatting](https://golang.org/doc/effective_go.html#formatting)
   guidelines (i.e. uses [gofmt](https://golang.org/cmd/gofmt/)).
 * Code must be documented adhering to the official Go [commentary](https://golang.org/doc/effective_go.html#commentary)
   guidelines.
 * Pull requests need to be based on and opened against the `master` branch.
 * Commit messages should be prefixed with the package(s) they modify.
   * E.g. "eth, rpc: make trace configs optional"

Please see the [Developers' Guide](https://geth.ethereum.org/docs/developers/geth-developer/dev-guide)
for more details on configuring your environment, managing project dependencies, and
testing procedures.

## License

The bsc library (i.e. all code outside of the `cmd` directory) is licensed under the
[GNU Lesser General Public License v3.0](https://www.gnu.org/licenses/lgpl-3.0.en.html),
also included in our repository in the `COPYING.LESSER` file.

The bsc binaries (i.e. all code inside of the `cmd` directory) is licensed under the
[GNU General Public License v3.0](https://www.gnu.org/licenses/gpl-3.0.en.html), also
included in our repository in the `COPYING` file.
