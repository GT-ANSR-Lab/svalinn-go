#!/bin/bash
# run with sudo

# Get the current directory path
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "$script_dir"

# Load CPUID and MSR module
modprobe cpuid
modprobe msr

# Useful for performance monitoring
sysctl -w kernel.nmi_watchdog=0
sysctl -w kernel.perf_event_paranoid=-1

# Networking settings
sysctl -w net.core.netdev_max_backlog=65535
sysctl -w net.ipv4.tcp_max_syn_backlog=65535
sysctl -w net.core.somaxconn=65535
sysctl -w net.ipv4.tcp_moderate_rcvbuf=0
sysctl -w net.ipv4.tcp_rmem="1048576 1048576 1048576"
sysctl -w net.ipv4.tcp_wmem="1048576 1048576 1048576"
grep -qxF "* soft nofile 200000" /etc/security/limits.conf || \
    echo "* soft nofile 200000" | sudo tee -a /etc/security/limits.conf
grep -qxF "* hard nofile 200000" /etc/security/limits.conf || \
    echo "* hard nofile 200000" | sudo tee -a /etc/security/limits.conf
grep -qxF "root soft nofile 200000" /etc/security/limits.conf || \
    echo "root soft nofile 200000" | sudo tee -a /etc/security/limits.conf
grep -qxF "root hard nofile 200000" /etc/security/limits.conf || \
    echo "root hard nofile 200000" | sudo tee -a /etc/security/limits.conf

# Turn off cstate
pkill -9 cstate
gcc "$script_dir/cstate.c" -o "$script_dir/cstate"
"$script_dir/cstate" 0 &

# Disable frequency scaling
echo performance | tee /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor

# Disable turbo boost
scaling_driver="$(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_driver)"
if [[ "$scaling_driver" == "intel_pstate" || "$scaling_driver" == "intel_cpufreq" ]]; then
    echo 1 | sudo tee /sys/devices/system/cpu/intel_pstate/no_turbo
elif [[ "$scaling_driver" == "acpi-cpufreq" ]]; then
    echo 0 | sudo tee /sys/devices/system/cpu/cpufreq/boost
fi
