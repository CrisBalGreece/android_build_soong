// Copyright (C) 2019 The Android Open Source Project
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package apex

import (
	"strings"

	"android/soong/android"
	"android/soong/cc"

	"github.com/google/blueprint/proptools"
)

const (
	vndkApexName       = "com.android.vndk"
	vndkApexNamePrefix = vndkApexName + ".v"
)

// apex_vndk creates a special variant of apex modules which contains only VNDK libraries.
// If `vndk_version` is specified, the VNDK libraries of the specified VNDK version are gathered automatically.
// If not specified, then the "current" versions are gathered.
func vndkApexBundleFactory() android.Module {
	bundle := newApexBundle()
	bundle.vndkApex = true
	bundle.AddProperties(&bundle.vndkProperties)
	android.AddLoadHook(bundle, func(ctx android.LoadHookContext) {
		ctx.AppendProperties(&struct {
			Compile_multilib *string
		}{
			proptools.StringPtr("both"),
		})
	})
	return bundle
}

func (a *apexBundle) vndkVersion(config android.DeviceConfig) string {
	vndkVersion := proptools.StringDefault(a.vndkProperties.Vndk_version, "current")
	if vndkVersion == "current" {
		vndkVersion = config.PlatformVndkVersion()
	}
	return vndkVersion
}

type apexVndkProperties struct {
	// Indicates VNDK version of which this VNDK APEX bundles VNDK libs. Default is Platform VNDK Version.
	Vndk_version *string
}

func apexVndkMutator(mctx android.TopDownMutatorContext) {
	if ab, ok := mctx.Module().(*apexBundle); ok && ab.vndkApex {
		if ab.IsNativeBridgeSupported() {
			mctx.PropertyErrorf("native_bridge_supported", "%q doesn't support native bridge binary.", mctx.ModuleType())
		}

		vndkVersion := ab.vndkVersion(mctx.DeviceConfig())
		// Ensure VNDK APEX mount point is formatted as com.android.vndk.v###
		ab.properties.Apex_name = proptools.StringPtr(vndkApexNamePrefix + vndkVersion)
	}
}

func apexVndkDepsMutator(mctx android.BottomUpMutatorContext) {
	if m, ok := mctx.Module().(*cc.Module); ok && cc.IsForVndkApex(mctx, m) {
		vndkVersion := m.VndkVersion()
		// For VNDK-Lite device, we gather core-variants of VNDK-Sp libraries, which doesn't have VNDK version defined
		if vndkVersion == "" {
			vndkVersion = mctx.DeviceConfig().PlatformVndkVersion()
		}
		if vndkVersion == mctx.DeviceConfig().PlatformVndkVersion() {
			vndkVersion = "current"
		} else {
			vndkVersion = "v" + vndkVersion
		}

		vndkApexName := "com.android.vndk." + vndkVersion

		if mctx.OtherModuleExists(vndkApexName) {
			mctx.AddReverseDependency(mctx.Module(), sharedLibTag, vndkApexName)
		}
	} else if a, ok := mctx.Module().(*apexBundle); ok && a.vndkApex {
		vndkVersion := proptools.StringDefault(a.vndkProperties.Vndk_version, "current")
		mctx.AddDependency(mctx.Module(), prebuiltTag, cc.VndkLibrariesTxtModules(vndkVersion)...)
	}
}

// name is module.BaseModuleName() which is used as LOCAL_MODULE_NAME and also LOCAL_OVERRIDES_*
func makeCompatSymlinks(name string, ctx android.ModuleContext, primaryApex bool) (symlinks android.InstallPaths) {
	// small helper to add symlink commands
	addSymlink := func(target string, dir android.InstallPath, linkName string) {
		if primaryApex {
			symlinks = append(symlinks, ctx.InstallAbsoluteSymlink(dir, linkName, target))
		} else {
			symlinks = append(symlinks, dir.Join(ctx, linkName))
		}
	}

	// TODO(b/142911355): [VNDK APEX] Fix hard-coded references to /system/lib/vndk
	// When all hard-coded references are fixed, remove symbolic links
	// Note that  we should keep following symlinks for older VNDKs (<=29)
	// Since prebuilt vndk libs still depend on system/lib/vndk path
	if strings.HasPrefix(name, vndkApexNamePrefix) {
		vndkVersion := strings.TrimPrefix(name, vndkApexNamePrefix)
		if ver, err := android.ApiLevelFromUser(ctx, vndkVersion); err != nil {
			ctx.ModuleErrorf("apex_vndk should be named as %v<ver:number>: %s", vndkApexNamePrefix, name)
			return
		} else if ver.GreaterThan(android.SdkVersion_Android10) {
			return
		}
		// the name of vndk apex is formatted "com.android.vndk.v" + version
		apexName := vndkApexNamePrefix + vndkVersion
		if ctx.Config().Android64() {
			dir := android.PathForModuleInPartitionInstall(ctx, "system", "lib64")
			addSymlink("/apex/"+apexName+"/lib64", dir, "vndk-sp-"+vndkVersion)
			addSymlink("/apex/"+apexName+"/lib64", dir, "vndk-"+vndkVersion)
		}
		if !ctx.Config().Android64() || ctx.DeviceConfig().DeviceSecondaryArch() != "" {
			dir := android.PathForModuleInPartitionInstall(ctx, "system", "lib")
			addSymlink("/apex/"+apexName+"/lib", dir, "vndk-sp-"+vndkVersion)
			addSymlink("/apex/"+apexName+"/lib", dir, "vndk-"+vndkVersion)
		}
	}

	// http://b/121248172 - create a link from /system/usr/icu to
	// /apex/com.android.i18n/etc/icu so that apps can find the ICU .dat file.
	// A symlink can't overwrite a directory and the /system/usr/icu directory once
	// existed so the required structure must be created whatever we find.
	if name == "com.android.i18n" {
		dir := android.PathForModuleInPartitionInstall(ctx, "system", "usr")
		addSymlink("/apex/com.android.i18n/etc/icu", dir, "icu")
	}

	// TODO(b/124106384): Clean up compat symlinks for ART binaries.
	if name == "com.android.art" {
		dir := android.PathForModuleInPartitionInstall(ctx, "system", "bin")
		addSymlink("/apex/com.android.art/bin/dalvikvm", dir, "dalvikvm")
		dex2oat := "dex2oat32"
		if ctx.Config().Android64() {
			dex2oat = "dex2oat64"
		}
		addSymlink("/apex/com.android.art/bin/"+dex2oat, dir, "dex2oat")
	} else if name == "com.android.art" || strings.HasPrefix(name, "com.android.art.") {
		dir := android.PathForModuleInPartitionInstall(ctx, "system", "bin")
		symlinks = append(symlinks,
			dir.Join(ctx, "dalvikvm"),
			dir.Join(ctx, "dex2oat"))
	}

	return symlinks
}
