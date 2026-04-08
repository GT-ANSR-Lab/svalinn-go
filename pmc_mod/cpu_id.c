#include <string.h>

#include "cpu_id.h"


struct cpuid_info {
	unsigned int eax, ebx, ecx, edx;
};

static inline void cpuid(int leaf, struct cpuid_info *regs)
{
	asm volatile("cpuid" : "=a" (regs->eax), "=b" (regs->ebx), "=c" (regs->ecx), "=d" (regs->edx) : "a" (leaf));
}


void CpuIdInfo_Get(CpuIdInfo *ci) {
    struct cpuid_info regs;
    char vendor[13];

    // Get the CPU vendor
    cpuid(0, &regs);
    memcpy(vendor + 0, &regs.ebx, 4);
    memcpy(vendor + 4, &regs.edx, 4);
    memcpy(vendor + 8, &regs.ecx, 4);
    vendor[12] = '\0';
    strcpy(ci->m_vendor, vendor);

    // Get the family and model of the CPU
    cpuid(1, &regs);
    uint32_t base_family = (regs.eax >> 8) & 0xF;
    uint32_t base_model  = (regs.eax >> 4) & 0xF;
    uint32_t ext_family  = (regs.eax >> 20) & 0xFF;
    uint32_t ext_model   = (regs.eax >> 16) & 0xF;
    ci->m_family = base_family;
    if (base_family == 0xF) {
        ci->m_family += ext_family;
    }
    ci->m_model = base_model;
    if (base_family == 0x6 || base_family == 0xF) {
        ci->m_model += (ext_model << 4);
    }
}


bool CpuIdInfo_IsIntel(CpuIdInfo *ci) {
    return !strcmp(ci->m_vendor, "GenuineIntel");
}


bool CpuIdInfo_IsAmd(CpuIdInfo *ci) {
    return !strcmp(ci->m_vendor, "AuthenticAMD");
}


bool CpuIdInfo_IsAmdZen2(CpuIdInfo *ci) {
    return !strcmp(ci->m_vendor, "AuthenticAMD") &&
        ci->m_family == 0x17 &&
        ci->m_model >= 0x30 && ci->m_model <= 0x3F;

}


bool CpuIdInfo_IsAmdZen3(CpuIdInfo *ci) {
    return !strcmp(ci->m_vendor, "AuthenticAMD") &&
        ci->m_family == 0x19 &&
        ci->m_model >= 0x00 && ci->m_model <= 0x0F;

}


bool CpuIdInfo_IsAmdZen4(CpuIdInfo *ci) {
    return !strcmp(ci->m_vendor, "AuthenticAMD") &&
        ci->m_family == 0x19 &&
        ((ci->m_model >= 0x10 && ci->m_model <= 0x1F) ||
         (ci->m_model >= 0xA0 && ci->m_model <= 0xAF));
}
