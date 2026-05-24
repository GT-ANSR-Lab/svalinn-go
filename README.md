# Svalinn for Go

This repository has the code for `Svalinn` overload control system for Go-based applications as evaluated in `Svalinn` paper (OSDI '26).

## Deploying the testbed

`Svalinn` is tested extensively on `xl170` and `c6525-25g` machines on [Cloudlab](https://cloudlab.us/). This [Cloudlab Profile](https://www.cloudlab.us/p/CC-Profiler/1-type-nodes-sh) can be used to deploy a cluster of nodes for evaluating `Svalinn`. Deploying the profile requires the user to set three options
1. **OS image** - Select `Shenango Image (Ubuntu 24.04 + Linux Kernel 6.8)`. This image installs `Ubuntu 24.04` with `Linux kernel 6.8` on all the nodes in the testbed. `Svalinn` is tested extensively on this OS + Kernel combination. This image also comes pre-packaged with the required `Mellanox OFED drivers`. The Cloudlab image used is `urn:publicid:IDN+utah.cloudlab.us+image+cc-profiler-PG0:shubuntu24linux68mlnxofed`.
2. **Number of Nodes** - At least 2, 1 server and 1 client (preferrably 11, 1 server and 10 clients, to be as close to the paper)
3. **Node Type** - Select `Cloudlab Utah` and then select either `xl170` or `c6525-25g`. (preferrably `xl170` to be as close to the paper)

## Setting up the testbed

Perform the following steps in order to ensure the deployed testbed is made ready to evaluate `Svalinn`.

### SSH configuration

Create SSH public-private key pairs on all the nodes using `ssh-keygen -t rsa`. Copy the public key `~/.ssh/id_rsa.pub` of the **initiator** machine (the machine from where you will clone the repository and run the configuration and test scripts) to the `~/.ssh/authorized_keys` file on all the nodes. This machine can be your local laptop or one of the deployed nodes on Cloudlab. This machine must have the `paramiko` python3 library installed. If using one of the deployed nodes as the **initiator**, copy the `~/.ssh/id_rsa.pub` of this selected node to the `~/.ssh/authorized_keys` file on all the nodes, **including itself**.

### Clone the repository

Clone the repository using `git clone https://github.com/GT-ANSR-Lab/svalinn-go.git` on the **initiator** machine.

### Provide information of the deployed testbed

All of the configuration and test scripts are housed in `svalinn-go/scripts` directory. Before you can run any script, you must provide the information of the deployed Cloudlab nodes in the `config_remote.py` file. The following things need to be configured in this file
1. `NODES` - List of deployed Cloudlab nodes. Each node will have an entry (in the format of a python dictionary). You need to configure the name of the node (can be found using `uname -a` on the node); the node type (can be either `xl170` or `c6525-25g`); the NUMA node used to run the system; the number of CPU cores used to run the system; PCIe address of the test NIC (can be found using `ethtool -i <interface_name>`) (note that, this NIC is different than the management NIC); IP address, Netmask, and Gateway of the test NIC (can be found using `ifconfig -a`).
2. `SERVERS` - List of nodes to be used as servers. We only need 1 node as as server. Hence, this list should contain only one node from the `NODES` list. Note that, even though only one node is required, `SERVERS` still has to be a python list.
3. `CLIENTS` - List of nodes to be used as clients. Nodes not used as the servers can be used as the clients.
4. `USERNAME` - Your Cloudlab username (can be found using `echo $USER` on any node).
5. `KEY_LOCATION` - The path to the private SSH key on the **initiator** machine. If you are using one of the Cloudlab nodes as the **initiator**, then the path will be `~/.ssh/id_rsa`

This file is not supposed to be executed. You only need to provide the necessary information in this file. The current contents of `config_remote.py` provide an example of a Cloudlab testbed that consists of 12 nodes (11 `xl170` and 1 `c6525-25g`). The NUMA, core count, NIC PCIe address for `xl170` and `c6525-25g` can be seen in the script and you can use the same values if you plan to use these node types while deploying the testbed.

### Building Svalinn system

Now that the initial one time setup is done on the deployed nodes, we can build `Svalinn` overload control system. To do so, you need to go into the `svalinn-go/scripts` and run `setup_remote.py` python script. This script copies the required code over to all the deployed Cloudlab nodes and then build the system. Note that, you only need to run this once after deploying the testbed. If you do not reboot the nodes in between the tests, rerunning the `setup_remote.py` script is not required. However, you need to rerun the script if you reboot the nodes.

### Building Svalinn applications

`Svalinn` was evaluated with one application - netbench (synthetic). Each application might require its own configuration and building. Hence, every application has its own setup script. For example, if you wish to run netbench on `Svalinn`, then you need to run `setup_netbench.py` script. Note that these per-application scripts should be executed only after running `setup_remote.py`. You dont need to re-run the per-application setup scripts, unless you reboot the nodes.

## Running the tests

Once the system and the respective applications are built, we can run the tests. Each application has a test script named `run_*.py`. For example, netbench application has a script named `run_netbench.py`. To run the tests you can simply execute the python script corresponding to the required application. The python script takes no command line arguments. The required test settings can be configured by directly updating the variables in the script itself. The important options that need frequent updates are
1. `OVERLOAD_ALG` - The overload control algorithm to use. This can be `nocontrol`, `breakwater`, `seda`, `protego`, `pcc`. The baselines mentioned in the paper are SEDA and Protego, which can be selected using `seda` and `protego` respectively. For `Svalinn`, the value should be set to `pcc`.
2. `MSEM_ENABLE` - True, if the application should use the memory semaphore. False, otherwise. When the OVERLOAD_ALG is anything other than `pcc`, this option should be set to False. For `Svalinn` (i.e., `pcc`), this option can be set to True.
3. `NUM_CONNS` - Total number of client threads across all the client nodes. These threads generate the test traffic to the server.
4. `NUM_SAMPLES` and `MAX_OFFERED_LOAD` - The offered load points to test. If we set `MAX_OFFERED_LOAD` to 10000 and `NUM_SAMPLES` to 10, the script will run 10 test cases, each increasing the offered load by 1000 Requests Per Second (RPS), till we finally test for the offered load of 10000 RPS.
5. `SLO` - The latency SLO for the application.
6. Application specific config - Every application then has some specific configuration options that dictate the nature of the different requests and the request mix. `Svalinn` focuses mostly on CPU-bound and Memory bandwidth-bound requests, hence almost all test scripts have options to configure these two request types. The default values present in the script can be left to evaluate the setting used in the actual paper.

The overload control algorithm parameters are provided in per-algorithm configuration files housed in `ovld_configs` directory. Protego related configuration should be performed in `pg_config.go`. SEDA related configuration should be performed in `sd_config.go`. And finally, `Svalinn` related configuration should be performed in `pcc_config.go`. Each application will require some changes in the overload control algorithm parameters.

The results of the test run are dumped in the `outputs/<app_name>/<date>/<time>` subdirectory. Once, the test are done running, the exact path where the results are dumped is displayed for convenience. The most important file in this subdirectory would be the `output.csv` file. This file has an entry for every offered load sample the system was tested against. If `NUM_SAMPLES` was 10, this CSV file will have 10 rows.

## Source navigation

1. Svalinn's Utility-based Admission Controller - `svalinn-go/ovldctlrpc/pcc`
2. Svalinn's Memory Semaphore - `svalinn-go/msemaphore`
3. Evaluated applications - `svalinn-go/apps/*`
4. Hardware Performance Counter Measurement library - `svalinn-go/pmc_mod`
5. Linux network stack's socket buffer RX path delay estimator (eBPF) library - `svalinn-go/netdelay`
6. Performance thread (collects network stack delay and hardware performance counters on a dedicated core) - `svalinn-go/perf`
