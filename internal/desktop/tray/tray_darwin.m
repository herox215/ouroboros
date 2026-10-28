//go:build darwin && cgo

#import <Cocoa/Cocoa.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tray_darwin.h"

static NSStatusItem *ouroItem;
static id ouroTarget;

@interface OuroTrayTarget : NSObject
- (void)open:(id)sender;
@end
@implementation OuroTrayTarget
- (void)open:(id)sender {
    puts("open");
    fflush(stdout);
}
@end

void ouroTrayRun(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        ouroTarget = [[OuroTrayTarget alloc] init];
        ouroItem = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength] retain];
        ouroItem.button.title = @"◉";
        ouroItem.button.toolTip = @"Ouroboros";
        [NSApp run];
        [[NSStatusBar systemStatusBar] removeStatusItem:ouroItem];
        [ouroItem release];
        [ouroTarget release];
    }
}

void ouroTrayUpdate(const char *json) {
    @autoreleasepool {
        NSData *data = [[NSString stringWithUTF8String:json] dataUsingEncoding:NSUTF8StringEncoding];
        NSDictionary *status = [NSJSONSerialization JSONObjectWithData:data options:0 error:NULL];
        if (![status isKindOfClass:[NSDictionary class]]) return;
        dispatch_async(dispatch_get_main_queue(), ^{
            @autoreleasepool {
                NSString *label = status[@"Label"];
                ouroItem.button.title = [status[@"Attention"] boolValue] ? @"◉ !" : @"◉";
                ouroItem.button.toolTip = label;
                NSMenu *menu = [[[NSMenu alloc] init] autorelease];
                menu.autoenablesItems = NO;
                NSMenuItem *row = [menu addItemWithTitle:label action:NULL keyEquivalent:@""];
                row.enabled = NO;
                row = [menu addItemWithTitle:[NSString stringWithFormat:@"Running sessions (%@)", status[@"Running"]] action:NULL keyEquivalent:@""];
                row.enabled = NO;
                id sessions = status[@"Sessions"];
                if ([sessions isKindOfClass:[NSArray class]]) {
                    for (NSString *name in sessions) {
                        row = [menu addItemWithTitle:name action:NULL keyEquivalent:@""];
                        row.enabled = NO;
                    }
                }
                [menu addItem:[NSMenuItem separatorItem]];
                row = [menu addItemWithTitle:@"Open Ouroboros" action:@selector(open:) keyEquivalent:@""];
                row.target = ouroTarget;
                row.enabled = YES;
                ouroItem.menu = menu;
            }
        });
    }
}

void ouroTrayStop(void) {
    dispatch_async(dispatch_get_main_queue(), ^{ [NSApp terminate:nil]; });
}

// The helper is a private child. It runs before Go starts, so AppKit owns the
// initial thread and the child never becomes a node. A thread of its own reads
// one status per line from stdin; the end of stdin closes the helper, also when
// the parent exits unexpectedly.
__attribute__((constructor))
static void ouroTrayHelper(int argc, const char **argv) {
    if (argc != 2 || strcmp(argv[1], OURO_TRAY_HELPER_ARGUMENT) != 0) return;
    [NSThread detachNewThreadWithBlock:^{
        char *line = NULL;
        size_t size = 0;
        ssize_t n;
        while ((n = getline(&line, &size, stdin)) > 0) {
            if (line[n - 1] == '\n') line[n - 1] = 0;
            ouroTrayUpdate(line);
        }
        free(line);
        ouroTrayStop();
    }];
    ouroTrayRun();
    exit(0);
}
