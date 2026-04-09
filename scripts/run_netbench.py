#!/usr/bin/env python3

import paramiko
import os
from time import sleep, time
from util import *
from config_remote import *
from datetime import datetime
import random
import sys
import pandas as pd

################################
### Experiemnt Configuration ###
################################

# Overload controller settings
OVERLOAD_ALG = "nocontrol"

# Total number of client connections
NUM_CONNS = 100
# Total number of client machines (master and agents)
NUM_CLIENTS = len(CLIENTS)
# Total number of agents
NUM_AGENTS = len(AGENTS)

# List of offered load
NUM_SAMPLES = 1
MAX_OFFERED_LOAD = 10000
OFFERED_LOADS = [int((i+1) * (MAX_OFFERED_LOAD/NUM_SAMPLES)) for i in range(NUM_SAMPLES)]

# Network RTT on the testbed
NET_RTT = 10
SLO = 400

# Netbench settings
CPU_BOUND_WORK_ITR = 5000
MEM_BOUND_WORK_ITR = 100
CPU_BOUND_REQ_PCNT = 80

# Duration of a single test case (i.e., one offered load sample)
DURATION_SEC = 10

# Provides the opportunity to replace the files in all the machines
# Helps in testing quickly by updating the required files
FILES_TO_REPLACE = [
    # {
    #     "src": "server/netbench_server.go",
    #     "dst": "server/netbench_server.go",
    # },
]

############################
### End of configuration ###
############################

##################################
### Function definitions start ###
##################################

################################
### Function definitions end ###
################################

k = paramiko.RSAKey.from_private_key_file(KEY_LOCATION)

# connection to server
server_conn = paramiko.SSHClient()
server_conn.set_missing_host_key_policy(paramiko.AutoAddPolicy())
server_conn.connect(hostname = SERVERS[0]["name"], username = USERNAME, pkey = k)

# connection to client
client_conn = paramiko.SSHClient()
client_conn.set_missing_host_key_policy(paramiko.AutoAddPolicy())
client_conn.connect(hostname = CLIENT["name"], username = USERNAME, pkey = k)

# connections to agents
agent_conns = []
for agent in AGENTS:
    agent_conn = paramiko.SSHClient()
    agent_conn.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    agent_conn.connect(hostname = agent["name"], username = USERNAME, pkey = k)
    agent_conns.append(agent_conn)

# Clean-up environment
print("Cleaning up machines...")
cmd = "sudo pkill -9 netbench_server"
execute_remote([server_conn, client_conn] + agent_conns, cmd, True, False)
cmd = "sudo pkill -9 netbench_client"
execute_remote([server_conn, client_conn] + agent_conns, cmd, True, False)
sleep(1)

# Distribuing config files
print("Distributing configs...")
for node in NODES:
    cmd = "scp -P 22 -i {} -o StrictHostKeyChecking=no ./ovld_configs/nc_config.go"\
          " {}@{}:~/{}/ovldctlrpc/nocontrol/ >/dev/null"\
          .format(KEY_LOCATION, USERNAME, node["name"], ARTIFACT_PATH)
    execute_local(cmd)
    cmd = "scp -P 22 -i {} -o StrictHostKeyChecking=no ./ovld_configs/bw_config.go"\
          " {}@{}:~/{}/ovldctlrpc/breakwater/ >/dev/null"\
          .format(KEY_LOCATION, USERNAME, node["name"], ARTIFACT_PATH)
    execute_local(cmd)
    cmd = "scp -P 22 -i {} -o StrictHostKeyChecking=no ./ovld_configs/pg_config.go"\
          " {}@{}:~/{}/ovldctlrpc/protego/ >/dev/null"\
          .format(KEY_LOCATION, USERNAME, node["name"], ARTIFACT_PATH)
    execute_local(cmd)
    cmd = "scp -P 22 -i {} -o StrictHostKeyChecking=no ./ovld_configs/pcc_config.go"\
          " {}@{}:~/{}/ovldctlrpc/pcc/ >/dev/null"\
          .format(KEY_LOCATION, USERNAME, node["name"], ARTIFACT_PATH)
    execute_local(cmd)

# Replace the frequently updated files
print("Replacing files...")
for fil in FILES_TO_REPLACE:
    for node in NODES:
        cmd = "scp -P 22 -i {} -o StrictHostKeyChecking=no ./{}"\
              " {}@{}:~/{}/{} >/dev/null"\
              .format(KEY_LOCATION, fil["src"], USERNAME,
                      node["name"], ARTIFACT_PATH, fil["dst"])
        execute_local(cmd)

# Rebuild Go runtime
print("Building Go runtime...")
cmd = "GOROOT_BOOTSTRAP=~/{}/deps/bootstrap/go"\
    " bash -c 'cd ~/{}/go/src && ./make.bash'".\
    format(ARTIFACT_PATH, ARTIFACT_PATH)
execute_remote([server_conn, client_conn] + agent_conns, cmd, True)

# Build netbench
print("Building netbench...")
cmd = "cd ~/{}/apps/netbench && make clean && make all"\
        .format(ARTIFACT_PATH)
execute_remote([server_conn, client_conn] + agent_conns, cmd, True)

# Clean old test output files
print("Removing old output files...")
cmd = "rm ~/{0}/stdout.out ~/{0}/output.csv"\
      " >/dev/null 2>&1".format(ARTIFACT_PATH)
execute_remote([server_conn, client_conn] + agent_conns, cmd, True, False)

# Create output directory for this test run
curr_date = datetime.now().strftime("%m_%d_%Y")
curr_time = datetime.now().strftime("%H-%M-%S")
output_dir = "outputs/{}/{}".format(curr_date, curr_time)
if not os.path.isdir(output_dir):
   os.makedirs(output_dir)

# Generate the load
for offered_load in OFFERED_LOADS:

    print("Load = {:d}".format(offered_load))

    # Start netbench server
    print("\tStarting netbench server...")
    cmd = "cd ~/{} && GOMAXPROCS={}"\
        " sudo -E numactl --cpunodebind={} --membind={}"\
        " ./apps/netbench/build/netbench_server --ovldctlalgo {}"\
        " >stdout.out 2>&1".\
        format(ARTIFACT_PATH, SERVERS[0]["cores"], SERVERS[0]["numa"],
               SERVERS[0]["numa"], OVERLOAD_ALG)
    server_session = execute_remote([server_conn], cmd, False)[0]
    sleep(5)

    # Start netbench client
    print("\tExecuting netbench client...")
    client_agent_sessions = []
    cmd = "cd ~/{} && GOMAXPROCS={}"\
        " sudo -E numactl --cpunodebind={} --membind={}"\
        " ./apps/netbench/build/netbench_client --clienttype client --server {}"\
        " --ovldctlalgo {} --connections {} --agents {} --slo {} --load {}"\
        " --duration {} --cpuiters {} --memiters {} --cpuperc {}"\
        " >stdout.out 2>&1"\
        .format(ARTIFACT_PATH, CLIENT["cores"], CLIENT["numa"],
                CLIENT["numa"], SERVERS[0]["ip"], OVERLOAD_ALG, NUM_CONNS,
                NUM_CLIENTS, SLO, offered_load, DURATION_SEC,
                CPU_BOUND_WORK_ITR, MEM_BOUND_WORK_ITR, CPU_BOUND_REQ_PCNT)
    client_agent_sessions += execute_remote([client_conn], cmd, False)
    sleep(3)

    # Start netbench agents
    print("\tExecuting netbench agents...")
    for i in range(len(AGENTS)):
        cmd = "cd ~/{} && GOMAXPROCS={}"\
            " sudo -E numactl --cpunodebind={} --membind={}"\
            " ./apps/netbench/build/netbench_client --clienttype agent --master {}"\
            " >stdout.out 2>&1"\
            .format(ARTIFACT_PATH, AGENTS[i]["cores"], AGENTS[i]["numa"],
                    AGENTS[i]["numa"], CLIENT["ip"])
        client_agent_sessions += execute_remote([agent_conns[i]], cmd, False)

    # Wait for client and agents
    print("\tWaiting for netbench client and agents...")
    for client_agent_session in client_agent_sessions:
        client_agent_session.recv_exit_status()

    # Kill clients and server
    print("\tKilling netbench clients and server...")
    cmd = "sudo pkill -9 netbench_server"
    execute_remote([server_conn, client_conn] + agent_conns, cmd, True, False)
    cmd = "sudo pkill -9 netbench_client"
    execute_remote([server_conn, client_conn] + agent_conns, cmd, True, False)

    sleep(1)

# Close connections
server_conn.close()
client_conn.close()
for agent_conn in agent_conns:
    agent_conn.close()

print("Collecting outputs...")
# Collect the client stats
cmd = "scp -P 22 -i {} -o StrictHostKeyChecking=no {}@{}:~/{}/output.csv ./"\
        " >/dev/null".format(KEY_LOCATION, USERNAME, CLIENT["name"], ARTIFACT_PATH)
execute_local(cmd)
# Add the header to the raw output CSV file
header = "num_clients,offered_load,throughput,cpu_bound_req_throughput,mem_bound_req_throughput,goodput,cpu,min,mean,p50,cpu_bound_req_p50,mem_bound_req_p50,p90,cpu_bound_req_p90,mem_bound_req_p90,p99,cpu_bound_req_p99,mem_bound_req_p99,max,cpu_bound_req_st_p50,cpu_bound_req_st_p90,cpu_bound_req_st_p99,cpu_bound_req_st_mean,mem_bound_req_st_p50,mem_bound_req_st_p90,mem_bound_req_st_p99,mem_bound_req_st_mean,client:ecredit_rx_pps,client:cupdate_tx_pps,client:credit_expired_cps,client:resp_rx_pps,client:req_tx_pps,client:req_dropped_rps,server:cupdate_rx_pps,server:ecredit_tx_pps,server:credit_tx_cps,server:req_rx_pps,server:resp_tx_pps,server:req_drop_rate"
cmd = "echo \"{}\" > {}/output.csv".format(header, output_dir)
execute_local(cmd)
cmd = "cat output.csv >> {}/output.csv".format(output_dir)
execute_local(cmd)
cmd = "cp {}/output.csv output.csv".format(output_dir)
execute_local(cmd)

# Collect the stdout from the server
print("Collecting stdout of server...")
cmd = "rsync -azh --info=progress2 -e \"ssh -i {} -o StrictHostKeyChecking=no -o"\
        " UserKnownHostsFile=/dev/null\" {}@{}:~/{}/stdout.out {}/stdout.out.server >/dev/null"\
        .format(KEY_LOCATION, USERNAME, SERVERS[0]["name"], ARTIFACT_PATH, output_dir)
execute_local(cmd)

# Collect the stdout from the client
print("Collecting stdout of client...")
cmd = "rsync -azh --info=progress2 -e \"ssh -i {} -o StrictHostKeyChecking=no -o"\
        " UserKnownHostsFile=/dev/null\" {}@{}:~/{}/stdout.out {}/stdout.out.client >/dev/null"\
        .format(KEY_LOCATION, USERNAME, CLIENT["name"], ARTIFACT_PATH, output_dir)
execute_local(cmd)

# Collect the config used by this test run
run_config = ""
run_config += "overload algorithm: {}\n".format(OVERLOAD_ALG)
run_config += "number of nodes: {}\n".format(len(NODES))
run_config += "number of client nodes: {}\n".format(len(CLIENTS))
run_config += "number of agent nodes: {}\n".format(len(AGENTS))
run_config += "number of connections: {}\n".format(NUM_CONNS)
run_config += "offered load (in RPS): {}\n".format(OFFERED_LOADS)
run_config += "test duration (in seconds): {}\n".format(DURATION_SEC)
run_config += "RTT: {} us\n".format(NET_RTT)
run_config += "SLO: {} us\n".format(SLO)
run_config += "CPU-bound workload per-request iterations: {}\n".format(CPU_BOUND_WORK_ITR)
run_config += "Memory-bound workload per-request iterations: {}\n".format(MEM_BOUND_WORK_ITR)
run_config += "CPU-bound request percentage: {}\n".format(CPU_BOUND_REQ_PCNT)
cmd = "echo \"{}\" > {}/run.config".format(run_config, output_dir)
execute_local(cmd)

print("Output dumped at {}".format(output_dir))
print("Done.")
