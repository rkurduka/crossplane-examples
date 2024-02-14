package main

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/Rohitkuru/crossplane-examples/function-restapiwithlambda/input/v1beta1"
	apigateway "github.com/upbound/provider-aws/apis/apigateway/v1beta1"
	lambda "github.com/upbound/provider-aws/apis/lambda/v1beta1"

	"github.com/crossplane/function-sdk-go/errors"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1beta1 "github.com/crossplane/function-sdk-go/proto/v1beta1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/resource/composed"
	"github.com/crossplane/function-sdk-go/response"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Function returns whatever response you ask it to.
type Function struct {
	fnv1beta1.UnimplementedFunctionRunnerServiceServer

	log logging.Logger
}

// RunFunction runs the Function.
func (f *Function) RunFunction(_ context.Context, req *fnv1beta1.RunFunctionRequest) (*fnv1beta1.RunFunctionResponse, error) {
	f.log.Info("Running function", "tag", req.GetMeta().GetTag())

	rsp := response.To(req, response.DefaultTTL)

	// get input for function
	in := v1beta1.Input{}
	if err := request.GetInput(req, &in); err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot get input from %T", req))
		return rsp, nil
	}

	// TODO: Add your Function logic here!

	xr, err := request.GetObservedCompositeResource(req)

	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot get observed composite resource from %T", req))
		return rsp, nil
	}

	// Create an updated logger with useful information about the XR.
	log := f.log.WithValues(
		"xr-version", xr.Resource.GetAPIVersion(),
		"xr-kind", xr.Resource.GetKind(),
		"xr-name", xr.Resource.GetName(),
	)

	services, err := xr.Resource.GetValue("spec.services")

	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot get list of services from %T", req))
		return rsp, nil
	}

	type service map[string]string

	var servicelist []service

	switch reflect.TypeOf(services).Kind() {
	case reflect.Slice:

		s := reflect.ValueOf(services)

		for i := 0; i < s.Len(); i++ {
			route := s.Index(i)

			if route.Kind() == reflect.Interface {

				item := make(service)

				for key, value := range route.Interface().(map[string]interface{}) {

					_, exist := item[key]
					if !exist {

						item[key] = reflect.ValueOf(value).String()
					}

				}

				servicelist = append(servicelist, item)

			}

		}
		//myJson, _ := json.MarshalIndent(servicelist, "", "    ")
		//fmt.Println(string(myJson))

	}

	desired, err := request.GetDesiredComposedResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot get desired resources from %T", req))
		return rsp, nil
	}

	_ = lambda.AddToScheme(composed.Scheme)
	_ = apigateway.AddToScheme(composed.Scheme)

	allowed_region := []string{"us-east-1", "eu-central-1"}

	if !slices.Contains(allowed_region, in.Region) || !slices.Contains(allowed_region, in.Region) {
		fmt.Println(in.Region)
		err := fmt.Errorf("region not allowed")
		response.Fatal(rsp, errors.Wrapf(err, "invalid region"))
		return rsp, nil
	}

	rest_api := &apigateway.RestAPI{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"crossplane.io/external-name": in.Name,
			},
			Name: "xrestapi",
		},
		Spec: apigateway.RestAPISpec{
			ForProvider: apigateway.RestAPIParameters{
				Name:        &in.Name,
				Region:      &in.Region,
				Description: &in.Description,
			},
		},
	}

	cd_restapi, err := composed.From(rest_api)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", cd_restapi, &composed.Unstructured{}))
		return rsp, nil
	}
	desired[resource.Name("xrestapi")] = &resource.DesiredComposed{Resource: cd_restapi}

	//create apigateway deployment
	rest_api_deployment := &apigateway.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"crossplane.io/external-name": in.Name,
			},
			Name: "xlambdarestapideployment-" + in.Description,
		},
		Spec: apigateway.DeploymentSpec{
			ForProvider: apigateway.DeploymentParameters{
				Description: &in.Description,
				Region:      &in.Region,
				RestAPIID:   &rest_api.Name,
			},
		},
	}

	cd_restapi_deployment, err := composed.From(rest_api_deployment)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", cd_restapi_deployment, &composed.Unstructured{}))
		return rsp, nil
	}
	desired[resource.Name("xlambdarestapideployment")] = &resource.DesiredComposed{Resource: cd_restapi_deployment}

	//create restapi stage

	rest_api_stage := &apigateway.Stage{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"crossplane.io/external-name": in.Name,
			},
			Name: "xlambdarestapistage-" + in.Name,
		},
		Spec: apigateway.StageSpec{
			ForProvider: apigateway.StageParameters{
				DeploymentID: &rest_api_deployment.Name,
				Region:       &in.Region,
				RestAPIID:    &rest_api.Name,
				StageName:    &in.StageName,
			},
		},
	}

	cd_restapi_stage, err := composed.From(rest_api_stage)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", cd_restapi_stage, &composed.Unstructured{}))
		return rsp, nil
	}
	desired[resource.Name("xlambdarestapistage")] = &resource.DesiredComposed{Resource: cd_restapi_stage}

	for _, service := range servicelist {

		statmentid := "AllowFromApiGatewayInvokeLambda"
		action := "lambda:InvokeFunction"
		principal := "apigateway.amazonaws.com"
		http_method := "POST"
		proxy := "AWS_PROXY"

		var runtime string

		if service["runtime"] == "python" || service["runtime"] == "PYTHON" {
			runtime = "python3.8"
		}

		handler := "my_lambda_using_crossplane.lambda_handler"
		publish := true
		role := "arn:aws:iam::735820197821:role/lambda-assume-role-for-s3-access"
		s3Bucket := "myfirstbucketysingcrossplane"
		s3Key := "my-first-lambda.zip"

		pathset := service["path"]

		lambda_function := &lambda.Function{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"crossplane.io/external-name": service["name"],
				},
				Name: "xlambdafunction-" + service["name"],
			},
			Spec: lambda.FunctionSpec{
				ForProvider: lambda.FunctionParameters{
					Region:   &in.Region,
					Runtime:  &runtime,
					Handler:  &handler,
					Publish:  &publish,
					Role:     &role,
					S3Bucket: &s3Bucket,
					S3Key:    &s3Key,
				},
			},
		}

		cd, err := composed.From(lambda_function)
		if err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", cd, &composed.Unstructured{}))
			return rsp, nil
		}

		desired[resource.Name("xlambdafunction-"+service["name"])] = &resource.DesiredComposed{Resource: cd}

		lambda_permission := &lambda.Permission{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"crossplane.io/external-name": service["name"],
				},
				Name: "xlambdafunctionpermission-" + service["name"],
			},
			Spec: lambda.PermissionSpec{
				ForProvider: lambda.PermissionParameters{
					StatementID:  &statmentid,
					Action:       &action,
					FunctionName: &lambda_function.Name,
					Region:       &in.Region,
					Principal:    &principal,
				},
			},
		}

		cd_permission, err := composed.From(lambda_permission)
		if err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", cd_permission, &composed.Unstructured{}))
			return rsp, nil
		}
		desired[resource.Name("xlambdafunctionpermission-"+service["name"])] = &resource.DesiredComposed{Resource: cd_permission}

		//create restapi resource

		rest_api_resource := &apigateway.Resource{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"crossplane.io/external-name": service["name"],
				},
				Name: "xlambdarestapiresource-" + service["name"],
			},
			Spec: apigateway.ResourceSpec{
				ForProvider: apigateway.ResourceParameters{
					PathPart:  &pathset,
					Region:    &in.Region,
					ParentID:  &rest_api.Name,
					RestAPIID: &rest_api.Name,
				},
			},
		}

		cd_restapi_resource, err := composed.From(rest_api_resource)
		if err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", cd_restapi_resource, &composed.Unstructured{}))
			return rsp, nil
		}
		desired[resource.Name("xrestapiresource-"+service["name"])] = &resource.DesiredComposed{Resource: cd_restapi_resource}

		// create rest api method

		rest_api_method := &apigateway.Method{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"crossplane.io/external-name": service["name"],
				},
				Name: "xlambdarestapimethod-" + service["name"],
			},
			Spec: apigateway.MethodSpec{
				ForProvider: apigateway.MethodParameters{
					HTTPMethod: &http_method,
					Region:     &in.Region,
					ResourceID: &rest_api_resource.Name,
					RestAPIID:  &rest_api.Name,
				},
			},
		}

		cd_restapi_method, err := composed.From(rest_api_method)
		if err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", cd_restapi_method, &composed.Unstructured{}))
			return rsp, nil
		}
		desired[resource.Name("xrestapiresource-"+service["name"])] = &resource.DesiredComposed{Resource: cd_restapi_method}

		// create rest api integration
		rest_api_integration := &apigateway.Integration{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"crossplane.io/external-name": service["name"],
				},
				Name: "xlambdarestapiintegration-" + service["name"],
			},
			Spec: apigateway.IntegrationSpec{
				ForProvider: apigateway.IntegrationParameters{
					HTTPMethod:            &rest_api_method.Name,
					Region:                &in.Region,
					ResourceID:            &rest_api_resource.Name,
					RestAPIID:             &rest_api.Name,
					IntegrationHTTPMethod: &http_method,
					URI:                   &lambda_function.Name,
					Type:                  &proxy,
				},
			},
		}

		cd_restapi_integration, err := composed.From(rest_api_integration)
		if err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot convert %T to %T", cd_restapi_integration, &composed.Unstructured{}))
			return rsp, nil
		}
		desired[resource.Name("xlambdarestapiintegration-"+service["name"])] = &resource.DesiredComposed{Resource: cd_restapi_integration}

	}

	if err := response.SetDesiredComposedResources(rsp, desired); err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot set desired composed resources in %T", rsp))
		return rsp, nil
	}

	log.Info("Added desired functions")

	return rsp, nil
}
