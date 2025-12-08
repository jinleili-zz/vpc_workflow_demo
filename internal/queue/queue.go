package queue

type DeviceType string

const (
	DeviceTypeSwitch       DeviceType = "switch"
	DeviceTypeLoadBalancer DeviceType = "loadbalancer"
	DeviceTypeFirewall     DeviceType = "firewall"
)

type TaskPriority int

const (
	PriorityLow      TaskPriority = 1
	PriorityNormal   TaskPriority = 3
	PriorityHigh     TaskPriority = 6
	PriorityCritical TaskPriority = 9
)

func GetTopicName(region, az string, deviceType DeviceType) string {
	return "tasks_" + region + "_" + az + "_" + string(deviceType)
}

func GetCallbackTopicName(region, az string) string {
	return "callbacks_" + region + "_" + az
}

func GetConsumerGroup(region, az string, deviceType DeviceType) string {
	return "worker_" + region + "_" + az + "_" + string(deviceType)
}

func GetCallbackConsumerGroup(region, az string) string {
	return "callback_" + region + "_" + az
}

func GetDeviceTypeForTaskType(taskType string) DeviceType {
	switch taskType {
	case "create_vrf_on_switch", "create_vlan_subinterface", "create_subnet_on_switch", "configure_subnet_routing":
		return DeviceTypeSwitch
	case "create_firewall_zone", "delete_firewall_zone":
		return DeviceTypeFirewall
	case "create_lb_pool", "delete_lb_pool", "configure_lb_listener":
		return DeviceTypeLoadBalancer
	default:
		return DeviceTypeSwitch
	}
}

type TaskOptions struct {
	Priority   TaskPriority
	DeviceType DeviceType
}

func DefaultTaskOptions() *TaskOptions {
	return &TaskOptions{
		Priority:   PriorityNormal,
		DeviceType: DeviceTypeSwitch,
	}
}
