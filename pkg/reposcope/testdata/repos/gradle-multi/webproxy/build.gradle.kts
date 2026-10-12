plugins {
    application
}

base {
    archivesName.set("webproxy-app")
}

dependencies {
    implementation(project(":webproxy-api"))
    implementation(projects.webproxyCore)
}
