package main

import "testing"

func TestEC2NamesFromJSON(t *testing.T) {
	data := []byte(`{"Reservations":[{"Instances":[
		{"InstanceId":"i-1","Tags":[{"Key":"Env","Value":"prod"},{"Key":"Name","Value":"bastion"}]},
		{"InstanceId":"i-2","Tags":[{"Key":"Env","Value":"dev"}]}
	]}]}`)
	names, ok := ec2NamesFromJSON(data)
	if !ok {
		t.Fatal("valid response rejected")
	}
	if got := names["i-1"]; got != "bastion" {
		t.Fatalf("i-1 Name = %q, want bastion", got)
	}
	if _, exists := names["i-2"]; exists {
		t.Fatal("instance without a Name tag must not be added")
	}
}

func TestEC2NamesFromJSONRejectsInvalidJSON(t *testing.T) {
	if _, ok := ec2NamesFromJSON([]byte(`{`)); ok {
		t.Fatal("invalid JSON accepted")
	}
}

func TestIsAWSAccessDenied(t *testing.T) {
	for _, message := range []string{
		"An error occurred (AccessDeniedException) when calling DescribeInstanceInformation",
		"An error occurred (UnauthorizedOperation) when calling DescribeInstances",
	} {
		if !isAWSAccessDenied(message) {
			t.Fatalf("access denial not detected: %s", message)
		}
	}
	if isAWSAccessDenied("RequestExpired: request has expired") {
		t.Fatal("unrelated AWS error detected as access denial")
	}
}
