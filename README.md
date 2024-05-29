# Ascend Kata Hook
-  **[项目介绍](#项目介绍)**
-  **[组件介绍](#组件介绍)**
-  **[编译Ascend-kata-hook](#编译Ascend-kata-hook)**
-  **[组件安装](#组件安装)**
-  **[更新日志](#更新日志)**

# 项目介绍
该项目fork自[Ascend docker runtime](https://github.com/Ascend/ascend-docker-runtime), 原项目设计以runtime的方式加载NPU卡，为更好支持kata runtime，进行了如下改造：
1. 去除runtime与cli，保留了hook
2. 将NPU设备挂在功能移植到hook中实现

# 组件介绍
该项目主要用于kata guest hook使用，用于将kata guest中的NPU 设备以及相关的驱动文件挂载到容器namespace中。
当前主要在Ascend 910b，910d上进行了测试。其它类型设备可以存在兼容性问题，如发现问题，欢迎提Issue，我们会第一时间支持。

## 设计简介

Ascend Kata Hook本质上是基于OCI标准实现的prestart hook，以插件方式提供Ascend NPU适配功能。
在容器完成创建，启动前，通过prestart完成NPU设备以及设备驱动的挂载


Ascend Kata Hook在prestart-hook这个钩子函数中，对容器做了以下配置操作：

* 将guest所有的NPU设备挂载到容器的namespace。
* 将guest上的驱动相关的文件、目录、以及设备符挂载到容器的namespace。
* 设置相应的环境变量。

# 编译Ascend-kata-hook
执行以下步骤进行编译

 1、下载master分支下的源码包，获得ascend-kata-hook
 
示例：源码放在/home/test/ascend-kata-hook目录下

```shell
git clone https://gitee.com/ascend/ascend-kata-hook.git
```

2、编译组件

```shell
cd ascend-kata-hook
go build -buildmode=pie -trimpath -o ./out/ascend-kata-hook ./hook/main.go
```

将编译后的组件：

- ascend-kata-hook

确保文件具备执行权限后放置到kata guest镜像的/usr/share/oci/hooks/pre-hook/目录下
详情参见[kata镜像制作](https://ai-study-room.github.io/docs/kata-npu/get-started/build/)

修改kata配置文件 

```shell
vim /etc/kata-containers/configuration.toml

增加如下配置项
guest_hook_path=/usr/share/oci/hooks/
```


# 更新日志
TBD
