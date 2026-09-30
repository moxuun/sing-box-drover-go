#import <Cocoa/Cocoa.h>
#import "wake_darwin.h"

extern void droverWakeCallback(void);

@interface DroverWakeObserver : NSObject
- (void)workspaceDidWake:(NSNotification *)notification;
@end

@implementation DroverWakeObserver
- (void)workspaceDidWake:(NSNotification *)notification {
	droverWakeCallback();
}
@end

static DroverWakeObserver *droverWakeObserver = nil;

void droverInstallWakeObserver(void) {
	if (droverWakeObserver != nil) {
		return;
	}
	droverWakeObserver = [[DroverWakeObserver alloc] init];
	[[[NSWorkspace sharedWorkspace] notificationCenter]
		addObserver:droverWakeObserver
		selector:@selector(workspaceDidWake:)
		name:NSWorkspaceDidWakeNotification
		object:nil];
}
