restart ?= all
focus ?= .\*

ifneq (,$(DAPPER_HOST_ARCH))

# Running in Dapper

include $(SHIPYARD_DIR)/Makefile.inc

TARGETS := $(shell ls -p scripts | grep -v -e / -e reload-images)
override BUILD_ARGS += $(shell source ${SCRIPTS_DIR}/lib/version; echo --ldflags \'-X main.VERSION=$${VERSION}\')

ifneq (,$(filter ovn,$(_using)))
override CLUSTERS_ARGS += --cluster_settings $(DAPPER_SOURCE)/scripts/cluster_settings.ovn
else
override CLUSTERS_ARGS += --cluster_settings $(DAPPER_SOURCE)/scripts/cluster_settings
endif

override E2E_ARGS += --focus $(focus) cluster2 cluster3 cluster1
override UNIT_TEST_ARGS += test/e2e
override VALIDATE_ARGS += --skip-dirs pkg/client

# Process extra flags from the `using=a,b,c` optional flag

# Targets to make

deploy: images

reload-images: build images
	./scripts/$@ --restart $(restart)

bin/submariner-engine: vendor/modules.txt main.go $(shell find pkg -not \( -path 'pkg/globalnet*' -o -path 'pkg/routeagent*' \))
	${SCRIPTS_DIR}/compile.sh $@ main.go $(BUILD_ARGS)

bin/submariner-route-agent: vendor/modules.txt $(shell find pkg/routeagent_driver)
	${SCRIPTS_DIR}/compile.sh $@ ./pkg/routeagent_driver/main.go $(BUILD_ARGS)

bin/submariner-globalnet: vendor/modules.txt $(shell find pkg/globalnet)
	${SCRIPTS_DIR}/compile.sh $@ ./pkg/globalnet/main.go $(BUILD_ARGS)

bin/submariner-networkplugin-syncer: vendor/modules.txt $(shell find pkg/networkplugin-syncer)
	${SCRIPTS_DIR}/compile.sh $@ ./pkg/networkplugin-syncer/main.go $(BUILD_ARGS)

build: bin/submariner-engine bin/submariner-route-agent bin/submariner-globalnet bin/submariner-networkplugin-syncer

ci: validate unit build images

images: build package/.image.submariner package/.image.submariner-route-agent package/.image.submariner-globalnet \
		package/.image.submariner-networkplugin-syncer

$(TARGETS): vendor/modules.txt
	./scripts/$@

.PHONY: $(TARGETS) build ci images unit validate

else

# Not running in Dapper

include Makefile.dapper

endif

# Disable rebuilding Makefile
Makefile Makefile.dapper Makefile.inc: ;
