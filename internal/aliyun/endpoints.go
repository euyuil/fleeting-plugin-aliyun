// ESS endpoint data adapted from alibaba-cloud-sdk-go v1.63.107.
// Copyright 2014 Alibaba.com. Licensed under Apache-2.0.
// Source: https://github.com/aliyun/alibaba-cloud-sdk-go/blob/v1.63.107/services/ess/endpoint.go
package aliyun

import "slices"

func endpoint(service, region string) string {
	if service == "ess" && slices.Contains([]string{"ap-northeast-2-pop", "ap-southeast-1", "cn-beijing", "cn-beijing-finance-pop", "cn-beijing-gov-1", "cn-beijing-nu16-b01", "cn-edge-1", "cn-fujian", "cn-haidian-cm12-c01", "cn-hangzhou", "cn-hangzhou-bj-b01", "cn-hangzhou-finance", "cn-hangzhou-internal-prod-1", "cn-hangzhou-internal-test-1", "cn-hangzhou-internal-test-2", "cn-hangzhou-internal-test-3", "cn-hangzhou-test-306", "cn-hongkong", "cn-hongkong-finance-pop", "cn-huhehaote-nebula-1", "cn-north-2-gov-1", "cn-qingdao", "cn-qingdao-nebula", "cn-shanghai", "cn-shanghai-et15-b01", "cn-shanghai-et2-b01", "cn-shanghai-finance-1", "cn-shanghai-inner", "cn-shanghai-internal-test-1", "cn-shenzhen", "cn-shenzhen-finance-1", "cn-shenzhen-inner", "cn-shenzhen-st4-d01", "cn-shenzhen-su18-b01", "cn-wuhan", "cn-yushanfang", "cn-zhangbei", "cn-zhangbei-na61-b01", "cn-zhangjiakou-na62-a01", "cn-zhengzhou-nebula-1", "eu-west-1-oxs", "rus-west-1-pop", "us-east-1", "us-west-1"}, region) {
		return "ess.aliyuncs.com"
	}
	return service + "." + region + ".aliyuncs.com"
}
