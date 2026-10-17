// Package e2e holds the end-to-end test of building and launching a
// custom image with real Packer, the real DigitalOcean Packer plugin and
// the real bivouac binary, against a fake DigitalOcean API whose droplets
// are QEMU virtual machines on this host.
//
// It is opt-in and never part of the default suite: it needs sudo, KVM,
// about 25 GB of disk and up to an hour. See TestImageBuildAndLaunch for
// how to run it.
package e2e
