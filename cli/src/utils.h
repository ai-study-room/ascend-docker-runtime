/*
 * Copyright (c) Huawei Technologies Co., Ltd. 2020-2020. All rights reserved.
 * Description: ascend-docker-cli工具实用函数模块头文件
*/
#ifndef _UTILS_H
#define _UTILS_H

#include <stdbool.h>
#include <sys/types.h>
#include "basic.h"

int IsStrEqual(const char *s1, const char *s2);
int StrHasPrefix(const char *str, const char *prefix);
int MkDir(const char *dir, int mode);
int VerifyPathInfo(const struct PathInfo* pathInfo);
int CheckDirExists(const char *dir);
int GetParentPathStr(const char *path, char *parent, size_t bufSize);
int MakeDirWithParent(const char *path, mode_t mode);
int IsCreateFileSuccess(const char *path, mode_t mode);

#endif