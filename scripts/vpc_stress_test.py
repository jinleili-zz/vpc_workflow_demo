#!/usr/bin/env python3
"""
VPC批量创建压力测试脚本
功能：并发创建10000个VPC，并轮询查询创建状态直到全部完成
"""

import asyncio
import aiohttp
import time
import argparse
import uuid
from datetime import datetime
from dataclasses import dataclass, field
from typing import Dict, Set
from enum import Enum


class VPCStatus(Enum):
    PENDING = "pending"
    CREATING = "creating"
    RUNNING = "running"
    FAILED = "failed"
    UNKNOWN = "unknown"


@dataclass
class TestConfig:
    top_nsp_addr: str = "http://localhost:8080"
    vpc_count: int = 10000
    concurrent_create: int = 100
    concurrent_query: int = 50
    region: str = "cn-beijing"
    query_interval: int = 10
    batch_prefix: str = field(default_factory=lambda: datetime.now().strftime("%Y%m%d%H%M%S"))


@dataclass
class TestStats:
    total: int = 0
    create_success: int = 0
    create_failed: int = 0
    running: int = 0
    creating: int = 0
    failed: int = 0
    unknown: int = 0
    start_time: float = 0
    create_end_time: float = 0
    query_end_time: float = 0


class VPCStressTest:
    def __init__(self, config: TestConfig):
        self.config = config
        self.stats = TestStats(total=config.vpc_count)
        self.vpc_names: Dict[str, str] = {}
        self.pending_vpcs: Set[str] = set()
        self.completed_vpcs: Set[str] = set()
        self.failed_vpcs: Set[str] = set()

    def generate_vpc_name(self, index: int) -> str:
        return f"vpc-{self.config.batch_prefix}-{index:05d}"

    async def create_vpc(self, session: aiohttp.ClientSession, index: int) -> bool:
        vpc_name = self.generate_vpc_name(index)
        vrf_name = f"VRF-{self.config.batch_prefix}-{index:05d}"
        vlan_id = 100 + (index % 3900)
        firewall_zone = f"zone-{index % 100:03d}"

        payload = {
            "vpc_name": vpc_name,
            "region": self.config.region,
            "vrf_name": vrf_name,
            "vlan_id": vlan_id,
            "firewall_zone": firewall_zone
        }

        try:
            async with session.post(
                f"{self.config.top_nsp_addr}/api/v1/vpc",
                json=payload,
                timeout=aiohttp.ClientTimeout(total=30)
            ) as resp:
                result = await resp.json()
                if result.get("success"):
                    self.vpc_names[vpc_name] = result.get("vpc_id", "")
                    self.pending_vpcs.add(vpc_name)
                    self.stats.create_success += 1
                    return True
                else:
                    self.stats.create_failed += 1
                    print(f"[CREATE FAILED] {vpc_name}: {result.get('message', 'unknown error')}")
                    return False
        except Exception as e:
            self.stats.create_failed += 1
            print(f"[CREATE ERROR] {vpc_name}: {e}")
            return False

    async def query_vpc_status(self, session: aiohttp.ClientSession, vpc_name: str) -> VPCStatus:
        try:
            async with session.get(
                f"{self.config.top_nsp_addr}/api/v1/vpc/{vpc_name}/status",
                timeout=aiohttp.ClientTimeout(total=10)
            ) as resp:
                result = await resp.json()
                overall_status = result.get("overall_status", "unknown")

                if overall_status == "running":
                    return VPCStatus.RUNNING
                elif overall_status == "creating":
                    return VPCStatus.CREATING
                elif overall_status == "failed":
                    return VPCStatus.FAILED
                else:
                    return VPCStatus.UNKNOWN
        except Exception as e:
            return VPCStatus.UNKNOWN

    async def create_vpcs_batch(self, session: aiohttp.ClientSession, start: int, end: int):
        semaphore = asyncio.Semaphore(self.config.concurrent_create)

        async def create_with_semaphore(index: int):
            async with semaphore:
                return await self.create_vpc(session, index)

        tasks = [create_with_semaphore(i) for i in range(start, end)]
        await asyncio.gather(*tasks)

    async def query_pending_vpcs(self, session: aiohttp.ClientSession) -> int:
        if not self.pending_vpcs:
            return 0

        semaphore = asyncio.Semaphore(self.config.concurrent_query)
        vpcs_to_check = list(self.pending_vpcs)
        newly_completed = 0

        async def query_with_semaphore(vpc_name: str):
            nonlocal newly_completed
            async with semaphore:
                status = await self.query_vpc_status(session, vpc_name)

                if status == VPCStatus.RUNNING:
                    self.pending_vpcs.discard(vpc_name)
                    self.completed_vpcs.add(vpc_name)
                    self.stats.running += 1
                    newly_completed += 1
                    print(f"[RUNNING] {vpc_name} - VPC创建完成")
                elif status == VPCStatus.FAILED:
                    self.pending_vpcs.discard(vpc_name)
                    self.failed_vpcs.add(vpc_name)
                    self.stats.failed += 1
                    print(f"[FAILED] {vpc_name} - VPC创建失败")

        tasks = [query_with_semaphore(vpc_name) for vpc_name in vpcs_to_check]
        await asyncio.gather(*tasks)

        return newly_completed

    def print_progress(self, phase: str):
        elapsed = time.time() - self.stats.start_time
        print(f"\n{'='*60}")
        print(f"[{phase}] 耗时: {elapsed:.1f}s")
        print(f"  创建成功: {self.stats.create_success}/{self.stats.total}")
        print(f"  创建失败: {self.stats.create_failed}")
        print(f"  已完成(running): {self.stats.running}")
        print(f"  进行中(creating): {len(self.pending_vpcs)}")
        print(f"  执行失败(failed): {self.stats.failed}")
        if self.stats.create_success > 0:
            throughput = self.stats.create_success / elapsed
            print(f"  吞吐量: {throughput:.2f} VPC/s")
        print(f"{'='*60}\n")

    async def run(self):
        print(f"\n{'='*60}")
        print("VPC批量创建压力测试")
        print(f"{'='*60}")
        print(f"  Top NSP地址: {self.config.top_nsp_addr}")
        print(f"  VPC数量: {self.config.vpc_count}")
        print(f"  创建并发数: {self.config.concurrent_create}")
        print(f"  查询并发数: {self.config.concurrent_query}")
        print(f"  区域: {self.config.region}")
        print(f"  批次前缀: {self.config.batch_prefix}")
        print(f"  查询间隔: {self.config.query_interval}s")
        print(f"{'='*60}\n")

        self.stats.start_time = time.time()

        connector = aiohttp.TCPConnector(limit=200, limit_per_host=100)
        async with aiohttp.ClientSession(connector=connector) as session:
            print("[阶段1] 开始批量创建VPC...")

            batch_size = 1000
            for batch_start in range(0, self.config.vpc_count, batch_size):
                batch_end = min(batch_start + batch_size, self.config.vpc_count)
                print(f"  创建批次 {batch_start+1}-{batch_end}...")
                await self.create_vpcs_batch(session, batch_start, batch_end)
                print(f"  批次完成: 成功={self.stats.create_success}, 失败={self.stats.create_failed}")

            self.stats.create_end_time = time.time()
            create_duration = self.stats.create_end_time - self.stats.start_time
            print(f"\n[阶段1完成] 创建阶段耗时: {create_duration:.1f}s")
            print(f"  成功发起: {self.stats.create_success}")
            print(f"  发起失败: {self.stats.create_failed}")

            print(f"\n[阶段2] 开始轮询查询VPC状态...")
            print(f"  待查询VPC数量: {len(self.pending_vpcs)}")

            round_num = 0
            while self.pending_vpcs:
                round_num += 1
                pending_count = len(self.pending_vpcs)
                print(f"\n--- 第{round_num}轮查询 (待查询: {pending_count}) ---")

                newly_completed = await self.query_pending_vpcs(session)

                remaining = len(self.pending_vpcs)
                print(f"  本轮完成: {newly_completed}, 剩余: {remaining}")

                if remaining > 0:
                    print(f"  等待{self.config.query_interval}秒后继续查询...")
                    await asyncio.sleep(self.config.query_interval)

            self.stats.query_end_time = time.time()

        self.print_final_report()

    def print_final_report(self):
        total_duration = self.stats.query_end_time - self.stats.start_time
        create_duration = self.stats.create_end_time - self.stats.start_time
        query_duration = self.stats.query_end_time - self.stats.create_end_time

        print(f"\n{'='*60}")
        print("测试结果汇总")
        print(f"{'='*60}")
        print(f"\n[创建结果]")
        print(f"  总数: {self.stats.total}")
        print(f"  创建成功: {self.stats.create_success}")
        print(f"  创建失败: {self.stats.create_failed}")

        print(f"\n[最终状态]")
        print(f"  Running (完成): {self.stats.running}")
        print(f"  Failed (失败): {self.stats.failed}")

        if self.stats.create_success > 0:
            success_rate = self.stats.running / self.stats.create_success * 100
            print(f"  成功率: {success_rate:.2f}%")

        print(f"\n[性能指标]")
        print(f"  创建阶段耗时: {create_duration:.1f}s")
        print(f"  查询阶段耗时: {query_duration:.1f}s")
        print(f"  总耗时: {total_duration:.1f}s")

        if create_duration > 0:
            create_throughput = self.stats.create_success / create_duration
            print(f"  创建吞吐量: {create_throughput:.2f} VPC/s")

        if total_duration > 0 and self.stats.running > 0:
            overall_throughput = self.stats.running / total_duration
            print(f"  端到端吞吐量: {overall_throughput:.2f} VPC/s")

        print(f"\n{'='*60}")
        if self.stats.running == self.stats.create_success:
            print("测试通过: 所有VPC创建成功")
        else:
            print(f"测试完成: {self.stats.running}/{self.stats.create_success} VPC创建成功")
        print(f"{'='*60}\n")


def main():
    parser = argparse.ArgumentParser(description="VPC批量创建压力测试")
    parser.add_argument("--addr", default="http://localhost:8080", help="Top NSP地址")
    parser.add_argument("--count", type=int, default=10000, help="VPC数量")
    parser.add_argument("--concurrent-create", type=int, default=100, help="创建并发数")
    parser.add_argument("--concurrent-query", type=int, default=50, help="查询并发数")
    parser.add_argument("--region", default="cn-beijing", help="区域")
    parser.add_argument("--interval", type=int, default=10, help="查询间隔(秒)")
    parser.add_argument("--prefix", default=None, help="VPC名称前缀")

    args = parser.parse_args()

    config = TestConfig(
        top_nsp_addr=args.addr,
        vpc_count=args.count,
        concurrent_create=args.concurrent_create,
        concurrent_query=args.concurrent_query,
        region=args.region,
        query_interval=args.interval,
    )

    if args.prefix:
        config.batch_prefix = args.prefix

    test = VPCStressTest(config)
    asyncio.run(test.run())


if __name__ == "__main__":
    main()
