GO =		go

DESTDIR =
PREFIX =	/usr/local
BINDIR =	${PREFIX}/bin
MANDIR =	${PREFIX}/man

INSTALL =	install
INSTALL_PROGRAM=${INSTALL} -m 0555
INSTALL_MAN =	${INSTALL} -m 0444

all: plakar

plakar:
	${GO} build -v .

install:
	mkdir -p ${DESTDIR}${BINDIR}
	mkdir -p ${DESTDIR}${MANDIR}/man1
	${INSTALL_PROGRAM} plakar ${DESTDIR}${BINDIR}
	find cmd/plakar -iname \*.1 -exec \
		${INSTALL_MAN} {} ${DESTDIR}${MANDIR}/man1 \;

check: test
test:
	${GO} test ./...

PROTO_SRC_DIR := $(shell pwd)/proto
PROTOS := importer exporter storage
HOME := $(shell echo $$HOME)/

gen:
	for proto in $(PROTOS); do \
		cp connectors/grpc/$$proto/$$proto.proto .; \
		mkdir -p ./pkg/$$proto/; \
		docker run --rm -ti \
			-v `pwd`:/app \
			-w /app \
			rvolosatovs/protoc \
				--proto_path=/app \
				--go_out=./pkg/$$proto/ \
				--go_opt=paths=source_relative,M$$proto.proto=github.com/PlakarKorp/go-kloset-sdk/$$proto \
				--go-grpc_out=./pkg/$$proto/ \
				--go-grpc_opt=paths=source_relative,M$$proto.proto=github.com/PlakarKorp/go-kloset-sdk/$$proto \
				/app/$$proto.proto; \
		rm -f ./$$proto.proto; \
		mv ./pkg/$$proto/* ./connectors/grpc/$$proto/pkg/; \
		rm -rf ./pkg/$$proto; \
	done
	rm -rf ./pkg

.PHONY: all plakar install check test gen
