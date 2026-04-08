#ifndef __CPU_ID_H__
#define __CPU_ID_H__

#include <stdint.h>

typedef struct CpuIdInfo {
    char m_vendor[16];
    uint32_t m_family;
    uint32_t m_model;
} CpuIdInfo;

void CpuIdInfo_Get(CpuIdInfo *ci);
bool CpuIdInfo_IsIntel(CpuIdInfo *ci);
bool CpuIdInfo_IsAmd(CpuIdInfo *ci);
bool CpuIdInfo_IsAmdZen2(CpuIdInfo *ci);
bool CpuIdInfo_IsAmdZen3(CpuIdInfo *ci);
bool CpuIdInfo_IsAmdZen4(CpuIdInfo *ci);

#endif  // __CPU_ID_H__
