.PHONY: test verify manifests install kind-test
test:
	go test ./...
verify:
	gofmt -w $$(find api cmd internal -name '*.go')
	go test ./...
	go vet ./...
	git diff --check
manifests:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 crd paths=./api/... output:crd:artifacts:config=config/crd
install:
	kubectl apply -k config
kind-test:
	./hack/kind-test.sh
