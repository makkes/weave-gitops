package gitops

import (
	"context"
	"fmt"

	"github.com/weaveworks/weave-gitops/cmd/gitops/version"
	"github.com/weaveworks/weave-gitops/manifests"
	"github.com/weaveworks/weave-gitops/pkg/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type UninstallParams struct {
	Namespace string
	DryRun    bool
}

type UninstallError struct{}

func (e UninstallError) Error() string {
	return "errors occurred during uninstall; the original state of the cluster may not be completely restored"
}

func (g *Gitops) Uninstall(params UninstallParams) error {
	ctx := context.Background()
	if g.kube.GetClusterStatus(ctx) != kube.GitOpsInstalled {
		g.logger.Println("gitops is not fully installed... removing any partial installation\n")
	}

	errorOccurred := false

	g.logger.Actionf("Deleting Weave Gitops manifests")

	if !params.DryRun {
		wegoAppManifests, err := manifests.GenerateWegoAppManifests(manifests.WegoAppParams{Version: version.Version, Namespace: params.Namespace})
		if err != nil {
			return fmt.Errorf("error generating wego-app manifests, %w", err)
		}

		wegoManifests := append(wegoAppManifests, manifests.AppCRD)
		for _, m := range wegoManifests {
			if err := g.kube.Delete(ctx, m); err != nil {
				if !apierrors.IsNotFound(err) {
					g.logger.Printf("error applying wego-app manifest \n%s: %w", m, err)

					errorOccurred = true
				}
			}
		}
	}

	wegoConfig, err := g.kube.GetWegoConfig(ctx, params.Namespace)
	if err != nil {
		return fmt.Errorf("failed getting wego config: %w", err)
	}

	uninstallFlux := true
	if wegoConfig.FluxNamespace != params.Namespace {
		uninstallFlux = false
	}

	if uninstallFlux {
		if err := g.flux.Uninstall(params.Namespace, params.DryRun); err != nil {
			g.logger.Printf("received error uninstalling flux: %q, continuing with uninstall", err)

			errorOccurred = true
		}
	}

	if errorOccurred {
		return UninstallError{}
	}

	return nil
}
