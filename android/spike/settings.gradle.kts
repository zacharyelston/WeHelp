// Spike #127: a throwaway app proving the double-pinned Relay transport and
// an Android Keystore device key. Not the product app (#128).
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}
rootProject.name = "wehelp-spike"
include(":app")
