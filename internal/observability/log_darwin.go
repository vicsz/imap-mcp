//go:build darwin && cgo

package observability

/*
#include <os/log.h>
#include <os/object.h>
#include <stdlib.h>

static void imap_mail_mcp_log(int category, int failed, const char *message) {
	os_log_t logger = os_log_create("local.imap-mail-mcp", category == 0 ? "imap" : "server");
	os_log_with_type(logger,
		failed ? OS_LOG_TYPE_ERROR : OS_LOG_TYPE_DEFAULT,
		"%{public}s", message);
	os_release(logger);
}
*/
import "C"

import "unsafe"

func writePlatformEvent(category string, failed bool, message string) {
	messageCString := C.CString(message)
	defer C.free(unsafe.Pointer(messageCString))
	categoryValue := C.int(1)
	if category == categoryIMAP {
		categoryValue = 0
	}
	failedValue := C.int(0)
	if failed {
		failedValue = 1
	}
	C.imap_mail_mcp_log(categoryValue, failedValue, messageCString)
}
