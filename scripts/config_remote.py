###
### config_remote.py - configuration for remote servers
###

# Paremters for a few known machines
#
# xl170 (cloudlab)
#   type: "xl170"
#   numa: 0
#   cores: 18
#   nicpci: 0000:03:00.1
#
# c6525-25g (cloudlab)
#   type: "c6525-25g"
#   numa: 0
#   cores: 30
#   nicpci: 0000:41:00.0
#
# asta (gt cluster)
#   type: "asta"
#   numa: 0
#   cores: 45
#   nicpci: 0000:17:00.0
#
# gon (gt cluster)
#   type: "gon"
#   numa: 0
#   cores: 45
#   nicpci: 0000:17:00.1


# The IP, Netmask, and Gateway are the IPs that we want to configure
# in the Caladan runtime. This is not the machine's control IP address.

NODES = [
    {
        "name": "node-0.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "xl170",
        "numa": 0,
        "cores": 18,
        "nicpci": "0000:03:00.1",
        "ip": "192.168.1.100",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
    {
        "name": "node-1.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "xl170",
        "numa": 0,
        "cores": 18,
        "nicpci": "0000:03:00.1",
        "ip": "192.168.1.101",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
    {
        "name": "node-2.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "xl170",
        "numa": 0,
        "cores": 18,
        "nicpci": "0000:03:00.1",
        "ip": "192.168.1.102",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
    {
        "name": "node-3.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "xl170",
        "numa": 0,
        "cores": 18,
        "nicpci": "0000:03:00.1",
        "ip": "192.168.1.103",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
    {
        "name": "node-4.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "xl170",
        "numa": 0,
        "cores": 18,
        "nicpci": "0000:03:00.1",
        "ip": "192.168.1.104",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
    {
        "name": "node-5.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "xl170",
        "numa": 0,
        "cores": 18,
        "nicpci": "0000:03:00.1",
        "ip": "192.168.1.105",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
    {
        "name": "node-6.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "xl170",
        "numa": 0,
        "cores": 18,
        "nicpci": "0000:03:00.1",
        "ip": "192.168.1.106",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
    {
        "name": "node-7.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "xl170",
        "numa": 0,
        "cores": 18,
        "nicpci": "0000:03:00.1",
        "ip": "192.168.1.107",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
    {
        "name": "node-8.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "xl170",
        "numa": 0,
        "cores": 18,
        "nicpci": "0000:03:00.1",
        "ip": "192.168.1.108",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
    {
        "name": "node-9.bhaskar3-295354.cc-profiler-pg0.utah.cloudlab.us",
        "type": "c6525-25g",
        "numa": 0,
        "cores": 30,
        "nicpci": "0000:41:00.0",
        "ip": "192.168.1.109",
        "netmask": "255.255.255.0",
        "gateway": "192.168.1.1",
    },
]

# Public domain or IP of server
SERVERS = NODES[8:9]
# Public domain or IP of intemediate
INTNODES = []
# Public domain or IP of client and agents
CLIENTS = NODES[0:8]
# Public domain or IP of client
CLIENT = CLIENTS[0]
AGENTS = CLIENTS[1:]

# Public domain or IP of monitor
MONITOR = ""

# Username and SSH credential location to access
# the server, client, and agents via public IP
USERNAME = "bhaskar3"
KEY_LOCATION = "/users/bhaskar3/.ssh/id_rsa"

# Location of Shenango to be installed. With "", Shenango
# will be installed in the home direcotry
ARTIFACT_PARENT = ""

### End of config ###

ARTIFACT_PATH = ARTIFACT_PARENT
if ARTIFACT_PATH != "":
    ARTIFACT_PATH += "/"
ARTIFACT_PATH += "caladan-all-artifact"


