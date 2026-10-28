// The parent starts this binary again with this one argument to get the
// menu-bar helper. Go and Objective-C share it from here.
#define OURO_TRAY_HELPER_ARGUMENT "--ouro-internal-macos-tray"

void ouroTrayRun(void);
void ouroTrayUpdate(const char *json);
void ouroTrayStop(void);
