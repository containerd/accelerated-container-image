package iscsi

import "strconv"

type Errno int

// Error table
//
// https://linux.die.net/man/8/iscsiadm#ExitStatus
var iscsiadmErrors = [...]string{
	0:  "command executed successfully",
	1:  "generic error code",
	2:  "session could not be found",
	3:  "could not allocate resource for operation",
	4:  "connect problem caused operation to fail",
	5:  "generic iSCSI login failure",
	6:  "error accessing/managing iSCSI DB",
	7:  "invalid argument",
	8:  "connection timer exired while trying to connect",
	9:  "generic internal iscsid/kernel failure",
	10: "iSCSI logout failed",
	11: "iSCSI PDU timedout",
	12: "iSCSI transport module not loaded in kernel or iscsid",
	13: "did not have proper OS permissions to access iscsid or execute iscsiadm command",
	14: "transport module did not support operation",
	15: "session is logged in",
	16: "invalid IPC MGMT request",
	17: "iSNS service is not supported",
	18: "a read/write to iscsid failed",
	19: "fatal iSCSI login error",
	20: "could not connect to iscsid",
	21: "no records/targets/sessions/portals found to execute operation on",
	22: "could not lookup object in sysfs",
	23: "could not lookup host",
	24: "login failed due to authorization failure",
	25: "iSNS query failure",
	26: "iSNS registration/deregistration failed",
}

// Errors
const (
	EOK              Errno = iota // command executed successfully.
	EGENERIC                      // generic error code.
	ESESSNOTFOUND                 // session could not be found.
	ENOMEM                        // could not allocate resource for operation.
	ETRANS                        // connect problem caused operation to fail.
	ELOGIN                        // generic iSCSI login failure.
	EIDBM                         // error accessing/managing iSCSI DB.
	EINVAL                        // invalid argument.
	ETRANSTIMEOUT                 // connection timer exired while trying to connect.
	EINTERNAL                     // generic internal iscsid/kernel failure.
	ELOGOUT                       // iSCSI logout failed.
	EPDUTIMEOUT                   // iSCSI PDU timedout.
	ETRANSNOTFOUND                // iSCSI transport module not loaded in kernel or iscsid.
	EACCESS                       // did not have proper OS permissions to access iscsid or execute iscsiadm command.
	ETRANSCAPS                    // transport module did not support operation.
	ESESSEXISTS                   // session is logged in.
	EINVALIDMGMTREQ               // invalid IPC MGMT request.
	EISNSUNAVAILABLE              // iSNS service is not supported.
	EISCSIDCOMMERR                // a read/write to iscsid failed.
	EFATALLOGIN                   // fatal iSCSI login error.
	EISCSIDNOTCONN                // could not connect to iscsid.
	ENOOBJSFOUND                  // no records/targets/sessions/portals found to execute operation on.
	ESYSFSLOOKUP                  // could not lookup object in sysfs.
	EHOSTNOTFOUND                 // could not lookup host.
	ELOGINAUTHFAILED              // login failed due to authorization failure.
	EISNSQUERY                    // iSNS query failure.
	EISNSREGFAILED                // iSNS registration/deregistration failed.
)

func (e Errno) Error() string {
	if 0 <= int(e) && int(e) < len(iscsiadmErrors) {
		return iscsiadmErrors[e]
	}
	return "errno " + strconv.Itoa(int(e))
}
