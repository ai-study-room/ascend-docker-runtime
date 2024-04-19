# Ascend Kata Hook
-  **[项目介绍](#项目介绍)**
-  **[组件介绍](#组件介绍)**
-  **[编译Ascend-kata-hook](#编译Ascend-kata-hook)**
-  **[组件安装](#组件安装)**
-  **[更新日志](#更新日志)**

# 项目介绍
该项目fork自[Ascend docker runtime](https://github.com/Ascend/ascend-docker-runtime), 原项目设计以runtime的方式加载NPU卡，为更好支持kata runtime，进行了如下改造：
1. 去除runtime，保留了hook与cli
2. 将NPU设备挂在功能移植到hook中实现

# 组件介绍
该项目分为Hook与cli两部分。其中hook用于注册到kata runtime中，cli主要处理具体的挂载任务

## 设计简介

Ascend Kata Hook本质上是基于OCI标准实现的prestart hook，以插件方式提供Ascend NPU适配功能。
在容器完成创建，启动前，通过prestart完成NPU设备以及设备驱动的挂载



prestart hook是OCI定义的容器生存状态，即created状态到running状态的一个中间过渡所设置的钩子函数。在这个过渡状态，容器的namespace已经被创建，但容器的作业还没有启动，因此可以对容器进行设备挂载，cgroup配置等操作。这样随后启动的作业便可以使用到这些配置。
Ascend Kata Hook在prestart-hook这个钩子函数中，对容器做了以下配置操作：
1.根据ASCEND_VISIBLE_DEVICES，将对应的NPU设备挂载到容器的namespace。
2.将Host上的CANN Runtime Library挂载到容器的namespace。

# 编译Ascend-kata-hook
执行以下步骤进行编译

 1、下载master分支下的源码包，获得ascend-kata-hook
 
示例：源码放在/home/test/ascend-kata-hook目录下
```shell
git clone https://gitee.com/ascend/ascend-kata-hook.git
```

 2、下载tag为v1.1.10的安全函数库
````shell
cd /home/test/ascend-kata-hook/platform
git clone -b v1.1.10 https://gitee.com/openeuler/libboundscheck.git
````

3、下载makeself
```shell
cd ../opensource
git clone -b openEuler-22.03-LTS https://gitee.com/src-openeuler/makeself.git
tar -zxvf makeself/makeself-2.4.2.tar.gz
git clone https://gitee.com/song-xiangbing/makeself-header.git
cp makeself-header/makeself-header.sh makeself-release-2.4.2/
```
 4、编译
```shell
cd ../build
bash build.sh
```
编译完成后，会在output文件夹看到相应的二进制run包
```shell
root@#:/home/test/ascend-kata-hook/output# ll
...
-rwxr-xr-x  ... Ascend-docker-runtime_x.x.x_linux-x86_64.run*
```

# 组件安装
将编译后的组件：
- ascend-docker-hook
- ascend-docker-cli
确保文件具备执行权限后放置到kata guest 镜像的/usr/share/oci/hooks/pre-hook/目录下

修改kata配置文件 
···shell
vim /etc/kata-containers/configuration.toml
```

增加如下配置项
···
guest_hook_path=/usr/share/oci/hooks/
```


# 更新日志
TBD

TBD
