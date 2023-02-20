/*
Copyright 2023 The Dapr Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azureeventgrid_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	secretstore_env "github.com/dapr/components-contrib/secretstores/local/env"
	bindings_loader "github.com/dapr/dapr/pkg/components/bindings"
	secretstores_loader "github.com/dapr/dapr/pkg/components/secretstores"
	"github.com/dapr/dapr/pkg/runtime"
	dapr_testing "github.com/dapr/dapr/pkg/testing"
	daprsdk "github.com/dapr/go-sdk/client"
	"github.com/dapr/kit/logger"
	"github.com/dapr/kit/ptr"

	"github.com/dapr/components-contrib/tests/certification/embedded"
	"github.com/dapr/components-contrib/tests/certification/flow"
	"github.com/dapr/components-contrib/tests/certification/flow/sidecar"

	// "github.com/Azure/azure-sdk-for-go/sdk/azcore"
	// "github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	// "github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	// "github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	// armeventgrid "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid/v2"
)

regiterRbacPermissions := func(ctx flow.Context) error {
	output, err := exec.Command("/bin/sh", "sp_rbac_permissions.sh").Output()
	assert.Nil(t, err, "Error in sp_rbac_permissions.sh.:\n%s", string(output))
	return nil
}

// basicTest := func(ctx flow.Context) error {
// 	client, err := client.NewClientWithPort(fmt.Sprint(currentGrpcPort))
// 	if err != nil {
// 		panic(err)
// 	}
// 	defer client.Close()
// }

// sendAndReceive := func(metadata map[string]string, messages ...*watcher.Watcher) flow.Runnable {
// 	_, hasKey := metadata[messageKey]
// 	return func(ctx flow.Context) error {
// 		client, err := daprsdk.NewClientWithPort(strconv.Itoa(grpcPort))
// 		require.NoError(t, err, "dapr init failed")

// 		// Define what is expected
// 		outputmsg := make([]string, numMessages)
// 		for i := 0; i < numMessages; i++ {
// 			outputmsg[i] = fmt.Sprintf("output binding: Message %03d", i)
// 		}
// 		received.ExpectStrings(outputmsg...)
// 		time.Sleep(20 * time.Second)
// 		if !hasKey {
// 			metadata[messageKey] = uuid.NewString()
// 		}
// 		// Send events from output binding
// 		for _, msg := range outputmsg {
// 			ctx.Logf("Sending eventhub message: %q", msg)

// 			err := client.InvokeOutputBinding(
// 				ctx, &dapr.InvokeBindingRequest{
// 					Name:      "azure-eventhubs-binding",
// 					Operation: "create",
// 					Data:      []byte(msg),
// 					Metadata:  metadata,
// 				})
// 			require.NoError(ctx, err, "error publishing message")
// 		}

// 		// Assert the observed messages
// 		received.Assert(ctx, time.Minute)
// 		return nil
// 	}
// }

func TestEventGrid(t *testing.T) {
	ports, err := dapr_testing.GetFreePorts(2)
	assert.NoError(t, err)

	currentGRPCPort := ports[0]
	currentHTTPPort := ports[1]

	flow.New(t, "eventgrid binding authentication using service principal").
		Step("Register Rbac permissions", regiterRbacPermissions).
		Step(sidecar.Run(sidecarName,
			embedded.WithoutApp(),
			embedded.WithComponentsPath("./components/serviceprincipal"),
			embedded.WithDaprGRPCPort(currentGRPCPort),
			embedded.WithDaprHTTPPort(currentHTTPPort),
			componentRuntimeOptions(),
		)).
		Run()
}

