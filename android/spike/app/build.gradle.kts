plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.wehelp.spike"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.wehelp.spike"
        // Android 10: TLS 1.3 on by default in Conscrypt, and StrongBox APIs.
        minSdk = 29
        targetSdk = 35
        versionCode = 1
        versionName = "0.1-spike"
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    // Transport and keys use only the platform: plain SSLSocket, org.json and
    // the Keystore. The one dependency is Google's code scanner for the
    // pairing QR (no camera permission; Google Play services draws the UI).
    implementation("com.google.android.gms:play-services-code-scanner:16.1.0")
    testImplementation("junit:junit:4.13.2")
}
